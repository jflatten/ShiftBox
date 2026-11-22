#!/usr/bin/env python3
"""Offline SOCKS fixtures for the private Ohio Packer SSH tunnel."""

import json
import os
import socket
import stat
import struct
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path


BRIDGE = Path(__file__).with_name("eice-socks.py")
ACCOUNT = "668112512962"
ENDPOINT = "eice-00000000000000001"
VPC = "vpc-00000000000000002"
SUBNET = "subnet-00000000000000003"
GROUP = "sg-00000000000000004"
BUILDER = "i-00000000000000005"
TAG = "v1.0.0-rc.1"
ATTEMPT = "attempt-1"
ADDRESS = "10.98.0.70"
BANNER = b"SSH-2.0-fixture\r\n"

AWS_STUB = r"""#!/usr/bin/env python3
import json
import os
import sys

config = json.load(open(os.environ["AWS_FIXTURE_CONFIG"], encoding="utf-8"))
args = sys.argv[1:]
with open(os.environ["AWS_FIXTURE_TRACE"], "a", encoding="utf-8") as trace:
    trace.write(json.dumps(args) + "\n")
if args[:2] == ["sts", "get-caller-identity"]:
    print(json.dumps({"Account": config.get("account")}))
elif args[:2] == ["ec2", "describe-instance-connect-endpoints"]:
    print(json.dumps({"InstanceConnectEndpoints": config.get("endpoints")}))
elif args[:2] == ["ec2", "describe-instances"]:
    print(json.dumps({"Reservations": config.get("reservations")}))
elif args[:2] == ["ec2-instance-connect", "open-tunnel"]:
    if config.get("tunnel_failure"):
        print("DO_NOT_LEAK_AWS_CHILD", file=sys.stderr)
        sys.exit(1)
    with open(os.environ["AWS_FIXTURE_CHILD"], "w", encoding="utf-8") as pid:
        pid.write(str(os.getpid()))
    sys.stdout.buffer.write(b"SSH-2.0-fixture\r\n")
    sys.stdout.buffer.flush()
    while data := sys.stdin.buffer.read1(4096):
        sys.stdout.buffer.write(data)
        sys.stdout.buffer.flush()
else:
    sys.exit(2)
"""


class BridgeTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.config_path = self.directory / "aws-fixture.json"
        self.trace = self.directory / "aws-trace.jsonl"
        self.child_path = self.directory / "aws-child"
        self.port_path = self.directory / "port"
        aws = self.directory / "aws"
        aws.write_text(AWS_STUB, encoding="utf-8")
        aws.chmod(0o700)
        self.environment = {
            **os.environ,
            "PATH": str(self.directory) + os.pathsep + os.environ["PATH"],
            "AWS_FIXTURE_CONFIG": str(self.config_path),
            "AWS_FIXTURE_TRACE": str(self.trace),
            "AWS_FIXTURE_CHILD": str(self.child_path),
        }
        tags = [
            {"Key": "project", "Value": "shiftbox"},
            {"Key": "cluster_name", "Value": "shiftbox-publisher"},
            {"Key": "environment", "Value": "release-" + TAG},
            {"Key": "publisher_attempt", "Value": ATTEMPT},
        ]
        self.endpoint = {
            "InstanceConnectEndpointId": ENDPOINT,
            "InstanceConnectEndpointArn": f"arn:aws:ec2:us-east-2:{ACCOUNT}:instance-connect-endpoint/{ENDPOINT}",
            "OwnerId": ACCOUNT,
            "VpcId": VPC,
            "SubnetId": SUBNET,
            "IpAddressType": "ipv4",
            "State": "create-complete",
            "PreserveClientIp": False,
            "Tags": tags,
        }
        self.builder = {
            "InstanceId": BUILDER,
            "VpcId": VPC,
            "SubnetId": SUBNET,
            "InstanceType": "t3.medium",
            "PrivateIpAddress": ADDRESS,
            "State": {"Name": "running"},
            "Tags": tags,
            "SecurityGroups": [{"GroupId": GROUP}],
            "NetworkInterfaces": [{"PrivateIpAddress": ADDRESS, "Groups": [{"GroupId": GROUP}]}],
        }
        self.config = {
            "account": ACCOUNT,
            "endpoints": [self.endpoint],
            "reservations": [{"OwnerId": ACCOUNT, "Instances": [self.builder]}],
        }

    def update_config(self):
        self.config_path.write_text(json.dumps(self.config), encoding="utf-8")

    def run_bridge(self, **overrides):
        self.update_config()
        arguments = {
            "account-id": ACCOUNT,
            "endpoint-id": ENDPOINT,
            "vpc-id": VPC,
            "subnet-id": SUBNET,
            "security-group-id": GROUP,
            "subnet-cidr": "10.98.0.64/26",
            "candidate-tag": TAG,
            "attempt-id": ATTEMPT,
            "port-file": str(self.port_path),
            **overrides,
        }
        command = [sys.executable, str(BRIDGE)]
        for flag, value in arguments.items():
            command.extend(["--" + flag, value])
        process = subprocess.Popen(command, env=self.environment, stderr=subprocess.PIPE)
        self.addCleanup(self.stop, process)
        return process

    def stop(self, process):
        if process.poll() is None:
            process.terminate()
        try:
            _, stderr = process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            _, stderr = process.communicate(timeout=5)
        return stderr

    def start(self):
        process = self.run_bridge()
        deadline = time.monotonic() + 5
        while not self.port_path.exists() and process.poll() is None and time.monotonic() < deadline:
            time.sleep(0.01)
        if not self.port_path.exists():
            self.fail(f"Bridge never became ready. {self.stop(process).decode()}")
        self.assertEqual(stat.S_IMODE(self.port_path.stat().st_mode), 0o600)
        self.port = int(self.port_path.read_text(encoding="utf-8"))
        return process

    def connect(self):
        connection = socket.create_connection(("127.0.0.1", self.port), timeout=3)
        self.addCleanup(connection.close)
        return connection

    def calls(self):
        return [json.loads(row) for row in self.trace.read_text(encoding="utf-8").splitlines()]

    def request(self, target=ADDRESS, port=22, command=1, address_type=1):
        ip_bytes = {
            1: socket.inet_aton(target),
            3: b"\x07example",
            4: socket.inet_pton(socket.AF_INET6, "::1"),
        }[address_type]
        return bytes((5, command, 0, address_type)) + ip_bytes + struct.pack("!H", port)

    def read_exact(self, connection, size):
        result = bytearray()
        while len(result) < size:
            piece = connection.recv(size - len(result))
            if not piece:
                self.fail("Bridge disconnected before its SOCKS reply completed")
            result.extend(piece)
        return result

    def socks(self, connection, request=None):
        connection.sendall(b"\x05\x01\x00")
        self.assertEqual(self.read_exact(connection, 2), b"\x05\x00")
        connection.sendall(self.request() if request is None else request)
        return self.read_exact(connection, 10)

    def test_private_tcp22_stream_reconnects_with_fresh_checks(self):
        # A real loopback SOCKS exchange serves the banner and bidirectional bytes.
        self.start()
        for _ in range(2):
            with self.connect() as connection:
                self.assertEqual(self.socks(connection)[:2], b"\x05\x00")
                self.assertEqual(self.read_exact(connection, len(BANNER)), BANNER)
                connection.sendall(b"packer")
                self.assertEqual(self.read_exact(connection, 6), b"packer")
        calls = self.calls()
        self.assertEqual([call[:2] for call in calls], [
            ["sts", "get-caller-identity"],
            ["ec2", "describe-instance-connect-endpoints"],
            ["ec2", "describe-instances"],
            ["ec2-instance-connect", "open-tunnel"],
            ["ec2", "describe-instances"],
            ["ec2-instance-connect", "open-tunnel"],
        ])
        for lookup in calls[2::2]:
            for value in (ADDRESS, VPC, SUBNET, GROUP, ATTEMPT, "release-" + TAG):
                self.assertTrue(any(arg.endswith("=" + value) for arg in lookup), lookup)
        for tunnel in calls[3::2]:
            for flag, value in {
                "--instance-connect-endpoint-id": ENDPOINT,
                "--private-ip-address": ADDRESS,
                "--remote-port": "22",
                "--max-tunnel-duration": "2700",
                "--region": "us-east-2",
            }.items():
                self.assertEqual(tunnel[tunnel.index(flag) + 1], value)
            self.assertNotIn("--local-port", tunnel)
            self.assertNotIn("--no-verify-ssl", tunnel)

    def test_rejects_unsigned_wrong_account_or_endpoint_before_binding(self):
        # Startup fences the signed identity and exact protected attempt first.
        self.config["account"] = "888293065132"
        process = self.run_bridge()
        _, stderr = process.communicate(timeout=5)
        self.assertNotEqual(process.returncode, 0, stderr)
        self.assertFalse(self.port_path.exists())
        self.assertEqual(len(self.calls()), 1)
        self.config["account"] = ACCOUNT
        self.endpoint["PreserveClientIp"] = True
        process = self.run_bridge()
        _, stderr = process.communicate(timeout=5)
        self.assertNotEqual(process.returncode, 0, stderr)
        self.assertFalse(self.port_path.exists())
        self.assertEqual(self.calls()[-1][:2], ["ec2", "describe-instance-connect-endpoints"])

    def test_readiness_cannot_replace_existing_path(self):
        # A stale or redirected readiness path cannot be overwritten by a new listener.
        self.port_path.symlink_to(self.directory / "kept")
        process = self.run_bridge()
        _, stderr = process.communicate(timeout=5)
        self.assertNotEqual(process.returncode, 0, stderr)
        self.assertTrue(self.port_path.is_symlink())
        self.assertFalse((self.directory / "kept").exists())

    def test_denies_other_protocols_and_addresses_without_aws_reads(self):
        # Non-SSH requests or non-IPv4 destinations cannot reach the AWS resolver.
        self.start()
        for request in (
            self.request("10.98.0.4"),
            self.request("8.8.8.8"),
            self.request(port=443),
            self.request(command=2),
            self.request(address_type=3),
            self.request(address_type=4),
        ):
            with self.subTest(request=request), self.connect() as connection:
                self.assertNotEqual(self.socks(connection, request)[1], 0)
        with self.connect() as connection:
            connection.sendall(b"\x05\x01\x02")
            self.assertEqual(self.read_exact(connection, 2), b"\x05\xff")
        self.assertEqual(len(self.calls()), 2)

    def test_denies_ambiguous_public_or_retagged_builders(self):
        # Filter parameters alone do not authorize the CLI response as a builder.
        self.start()
        for changes in (
            {"PublicIpAddress": "18.1.1.1"},
            {"IamInstanceProfile": {"Arn": "unexpected"}},
            {"Tags": self.builder["Tags"][:-1]},
            {"SecurityGroups": [{"GroupId": GROUP}, {"GroupId": "unexpected"}]},
            {"NetworkInterfaces": [{"PrivateIpAddress": ADDRESS, "Groups": [{"GroupId": "unexpected"}]}]},
            {"InstanceType": "t3.large"},
        ):
            with self.subTest(changes=changes), self.connect() as connection:
                self.config["reservations"][0]["Instances"] = [self.builder | changes]
                self.update_config()
                self.assertEqual(self.socks(connection)[1], 2)
        self.config["reservations"][0]["Instances"] = [self.builder, self.builder]
        self.update_config()
        with self.connect() as connection:
            self.assertEqual(self.socks(connection)[1], 2)
        self.assertFalse(any(call[1] == "open-tunnel" for call in self.calls()))

    def test_reconnect_cannot_switch_to_a_different_builder(self):
        # A reused address in the same protected attempt does not grant new authority.
        self.start()
        with self.connect() as connection:
            self.assertEqual(self.socks(connection)[1], 0)
        self.builder["InstanceId"] = "i-00000000000000009"
        self.update_config()
        with self.connect() as connection:
            self.assertEqual(self.socks(connection)[1], 2)
        self.assertEqual(sum(call[1] == "open-tunnel" for call in self.calls()), 1)

    def test_tunnel_failure_is_reported_without_cloud_stderr(self):
        # Packer receives a failed SOCKS connection if AWS denies the tunnel.
        self.config["tunnel_failure"] = True
        process = self.start()
        with self.connect() as connection:
            self.assertEqual(self.socks(connection)[1], 5)
        self.assertNotIn(b"DO_NOT_LEAK_AWS_CHILD", self.stop(process))

    def test_cancellation_reaps_child_and_removes_readiness(self):
        # Workflow cancellation ends the child and withdraws the local readiness file.
        process = self.start()
        with self.connect() as connection:
            self.assertEqual(self.socks(connection)[1], 0)
            child_pid = int(self.child_path.read_text(encoding="utf-8"))
            self.stop(process)
        with self.assertRaises(ProcessLookupError):
            os.kill(child_pid, 0)
        self.assertFalse(self.port_path.exists())


if __name__ == "__main__":
    unittest.main()
