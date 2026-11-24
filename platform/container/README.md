# ShiftBox controller image

Build and check the Linux amd64 image from the repository root on a Mac.

```sh
platform/container/smoke.sh shiftbox-controller:development
```

The build exports the browser assets and embeds the macOS helper disk image in the
Linux amd64 executable. To build on Linux, first run
`platform/helper/macos/package.sh --portable <output directory>` on macOS, then pass
that directory as the second argument to `platform/container/smoke.sh`. Direct
Buildx invocations supply it with `--build-context macos-helper=<directory>`.
The native packaging test mounts the disk image and verifies the signed app,
configuration, and Applications shortcut. The controller build checks that the
disk image was embedded. The portable app asks for the installation HTTPS URL on
first launch. The runtime also includes Dex and PostgreSQL 17 client tools and
runs as UID and GID `10001`. Provide a persistent directory owned by this identity
at `/var/lib/shiftbox`.

The default command is `shiftbox serve --config /var/lib/shiftbox/config.json`. To run another ShiftBox command in the image, supply its arguments as the container command. Supply the controller configuration and installation state at runtime.

Tag each release build with its release identifier. Publish the image through the release operator's registry session and use the registry digest when deploying it.
