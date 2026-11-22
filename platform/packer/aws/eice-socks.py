#!/usr/bin/env python3
"""Connect Packer's local SOCKS5 SSH client to its private Ohio build guest."""

import argparse
import ipaddress
import json
import os
import re
import select
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import time
from pathlib import Path


REGION = "us-east-2"
SSH_PORT = 22
TUNNEL_SECONDS = 2700
HANDSHAKE_SECONDS = 10
PRIVATE_RANGES = tuple(map(ipaddress.IPv4Network, ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")))
RESOURCE_ID = r"(?:[0-9a-f]{8}|[0-9a-f]{17})"
VERSION = r"(?:0|[1-9][0-9]*)"


def arguments():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--subnet-cidr", type=ipaddress.IPv4Network, required=True)
    parser.add_argument("--port-file", type=Path, required=True)
    patterns = {
        "account-id": r"[0-9]{12}",
        "endpoint-id": "eice-" + RESOURCE_ID,
        "vpc-id": "vpc-" + RESOURCE_ID,
        "subnet-id": "subnet-" + RESOURCE_ID,
        "security-group-id": "sg-" + RESOURCE_ID,
        "candidate-tag": rf"v{VERSION}\.{VERSION}\.{VERSION}-rc\.[1-9][0-9]*",
        "attempt-id": r"[a-z0-9][a-z0-9-]{0,17}",
    }
    for flag in patterns:
        parser.add_argument("--" + flag, required=True)
    args = parser.parse_args()
    for flag, pattern in patterns.items():
        if not re.fullmatch(pattern, getattr(args, flag.replace("-", "_"))):
            parser.error("invalid --" + flag)
    if not 16 <= args.subnet_cidr.prefixlen <= 28 or not any(
        args.subnet_cidr.subnet_of(network) for network in PRIVATE_RANGES
    ):
        parser.error("--subnet-cidr must be a private AWS IPv4 subnet")
    return args


def aws_command(*args):
    return ["aws", *args, "--region", REGION, "--no-cli-pager", "--no-cli-auto-prompt",
            "--cli-connect-timeout", "5", "--cli-read-timeout", "5"]


def aws_json(*args):
    result = subprocess.run(
        aws_command(*args, "--no-paginate", "--output", "json"),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        check=True,
        timeout=10,
    )
    response = json.loads(result.stdout)
    if not isinstance(response, dict):
        raise ValueError("AWS metadata response was not an object")
    return response


def read_exact(sock, size, deadline):
    data = bytearray()
    while len(data) < size:
        sock.settimeout(max(0.001, deadline - time.monotonic()))
        chunk = sock.recv(size - len(data))
        if not chunk or time.monotonic() >= deadline:
            raise OSError("incomplete SOCKS handshake")
        data.extend(chunk)
    return bytes(data)


def reply(sock, code):
    sock.settimeout(3)
    sock.sendall(bytes((5, code, 0, 1, 0, 0, 0, 0, 0, 0)))


def socks_destination(sock, subnet):
    deadline = time.monotonic() + HANDSHAKE_SECONDS
    version, count = read_exact(sock, 2, deadline)
    methods = read_exact(sock, count, deadline)
    if version != 5 or 0 not in methods:
        sock.sendall(b"\x05\xff")
        return None
    sock.sendall(b"\x05\x00")

    version, command, reserved, address_type = read_exact(sock, 4, deadline)
    if address_type == 1:
        address = ipaddress.IPv4Address(read_exact(sock, 4, deadline))
    elif address_type in (3, 4):
        size = read_exact(sock, 1, deadline)[0] if address_type == 3 else 16
        read_exact(sock, size + 2, deadline)
        reply(sock, 8)
        return None
    else:
        reply(sock, 8)
        return None
    port = int.from_bytes(read_exact(sock, 2, deadline), "big")
    if (version, command, reserved) != (5, 1, 0):
        reply(sock, 7)
        return None
    if port != SSH_PORT or address not in subnet or address in (
        subnet.network_address, subnet.broadcast_address
    ):
        reply(sock, 2)
        return None
    return str(address)


class Bridge:
    def __init__(self, args):
        self.args = args
        self.tags = {
            "project": "shiftbox",
            "cluster_name": "shiftbox-publisher",
            "environment": "release-" + args.candidate_tag,
            "publisher_attempt": args.attempt_id,
        }
        self.stopping = False
        self.target = None

    def stop(self, *_):
        self.stopping = True

    def has_tags(self, entry):
        observed = {tag["Key"]: tag["Value"] for tag in entry.get("Tags", [])}
        return all(observed.get(key) == value for key, value in self.tags.items())

    def verify_context(self):
        args = self.args
        if aws_json("sts", "get-caller-identity").get("Account") != args.account_id:
            raise ValueError("AWS identity did not match the approved publisher")
        result = aws_json(
            "ec2", "describe-instance-connect-endpoints", "--instance-connect-endpoint-ids",
            args.endpoint_id,
        )
        endpoints = result.get("InstanceConnectEndpoints", [])
        expected_arn = (
            f"arn:aws:ec2:{REGION}:{args.account_id}:instance-connect-endpoint/{args.endpoint_id}"
        )
        if result.get("NextToken") or len(endpoints) != 1:
            raise ValueError("the approved Instance Connect Endpoint was not unique")
        endpoint = endpoints[0]
        if (
            endpoint.get("InstanceConnectEndpointId") != args.endpoint_id
            or endpoint.get("InstanceConnectEndpointArn") != expected_arn
            or endpoint.get("OwnerId") != args.account_id
            or endpoint.get("VpcId") != args.vpc_id
            or endpoint.get("SubnetId") != args.subnet_id
            or endpoint.get("IpAddressType") != "ipv4"
            or endpoint.get("State") not in {"create-complete", "update-complete"}
            or endpoint.get("PreserveClientIp") is not False
            or not self.has_tags(endpoint)
        ):
            raise ValueError("Instance Connect Endpoint did not match the approved build attempt")

    def resolve_builder(self, address):
        args = self.args
        filters = {
            "private-ip-address": address,
            "vpc-id": args.vpc_id,
            "subnet-id": args.subnet_id,
            "instance.group-id": args.security_group_id,
            "instance-state-name": "running",
            **{f"tag:{key}": value for key, value in self.tags.items()},
        }
        result = aws_json(
            "ec2", "describe-instances", "--filters",
            *(f"Name={key},Values={value}" for key, value in filters.items()),
        )
        matches = [
            (reservation.get("OwnerId"), instance)
            for reservation in result.get("Reservations", [])
            for instance in reservation.get("Instances", [])
        ]
        if result.get("NextToken") or len(matches) != 1:
            return None
        owner, instance = matches[0]
        groups = [group.get("GroupId") for group in instance.get("SecurityGroups", [])]
        interfaces = instance.get("NetworkInterfaces", [])
        if (
            owner != args.account_id
            or instance.get("VpcId") != args.vpc_id
            or instance.get("SubnetId") != args.subnet_id
            or instance.get("PrivateIpAddress") != address
            or instance.get("State", {}).get("Name") != "running"
            or instance.get("InstanceType") != "t3.medium"
            or groups != [args.security_group_id]
            or instance.get("PublicIpAddress")
            or instance.get("PublicDnsName")
            or instance.get("IamInstanceProfile")
            or not self.has_tags(instance)
            or len(interfaces) != 1
        ):
            return None
        interface = interfaces[0]
        if (
            interface.get("PrivateIpAddress") != address
            or [group.get("GroupId") for group in interface.get("Groups", [])] != [args.security_group_id]
            or interface.get("Association")
            or interface.get("Ipv6Addresses")
        ):
            return None
        builder_id = instance.get("InstanceId", "")
        if re.fullmatch("i-" + RESOURCE_ID, builder_id):
            return builder_id, address
        return None

    def wait_readable(self, sockets, deadline):
        while not self.stopping and (remaining := deadline - time.monotonic()) > 0:
            ready, _, _ = select.select(sockets, [], [], min(0.2, remaining))
            if ready:
                return ready
        return []

    def tunnel(self, client, address):
        deadline = time.monotonic() + TUNNEL_SECONDS
        local, stdio = socket.socketpair()
        with local, stdio:
            process = subprocess.Popen(
                aws_command(
                    "ec2-instance-connect", "open-tunnel", "--instance-connect-endpoint-id",
                    self.args.endpoint_id, "--private-ip-address", address, "--remote-port",
                    str(SSH_PORT), "--max-tunnel-duration", str(TUNNEL_SECONDS),
                ),
                stdin=stdio,
                stdout=stdio,
                stderr=subprocess.DEVNULL,
            )
            stdio.close()
            try:
                # Wait for guest SSH bytes before reporting a successful SOCKS connection.
                ready = self.wait_readable([local], time.monotonic() + 15)
                banner = local.recv(65536) if ready else b""
                if not banner:
                    reply(client, 5)
                    print("Private Packer SSH tunnel did not open", file=sys.stderr)
                    return
                reply(client, 0)
                client.sendall(banner)
                local.settimeout(3)
                while ready := self.wait_readable([client, local], deadline):
                    for source in ready:
                        data = source.recv(65536)
                        if not data:
                            return
                        (local if source is client else client).sendall(data)
            finally:
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=2)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()

    def handle(self, client):
        address = socks_destination(client, self.args.subnet_cidr)
        if address is None or self.stopping:
            return
        try:
            target = self.resolve_builder(address)
        except (OSError, subprocess.SubprocessError, ValueError, TypeError, KeyError):
            reply(client, 5)
            print("Private Packer guest could not be verified", file=sys.stderr)
            return
        if target is None or (self.target is not None and target != self.target):
            reply(client, 2)
            return
        self.target = target
        if not self.stopping:
            self.tunnel(client, address)

    def serve(self):
        port_file = self.args.port_file
        directory = port_file.parent.stat(follow_symlinks=False)
        if (
            not port_file.is_absolute()
            or not stat.S_ISDIR(directory.st_mode)
            or directory.st_uid != os.geteuid()
            or directory.st_mode & 0o077
        ):
            raise ValueError("readiness file needs a private absolute parent directory")
        self.verify_context()
        if self.stopping:
            return
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
            listener.bind(("127.0.0.1", 0))
            listener.listen(4)
            port = listener.getsockname()[1]
            # Publish an already-written 0600 file atomically and reject existing paths.
            with tempfile.NamedTemporaryFile(dir=port_file.parent) as pending:
                pending.write(f"{port}\n".encode("ascii"))
                pending.flush()
                os.link(pending.name, port_file)
            try:
                listener.settimeout(0.2)
                while not self.stopping:
                    try:
                        client, _ = listener.accept()
                    except TimeoutError:
                        continue
                    with client:
                        try:
                            self.handle(client)
                        except OSError:
                            # Packer may close a connection while retrying SSH.
                            continue
            finally:
                port_file.unlink(missing_ok=True)


def main():
    args = arguments()
    os.environ.update(AWS_PAGER="", AWS_CLI_AUTO_PROMPT="off", AWS_IGNORE_CONFIGURED_ENDPOINT_URLS="true")
    bridge = Bridge(args)
    signal.signal(signal.SIGTERM, bridge.stop)
    signal.signal(signal.SIGINT, bridge.stop)
    try:
        bridge.serve()
    except (OSError, subprocess.SubprocessError, ValueError, TypeError, KeyError):
        print("Private Packer tunnel startup checks failed", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
