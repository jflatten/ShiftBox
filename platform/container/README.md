# ShiftBox controller image

Build and check the Linux amd64 image from the repository root.

```sh
platform/container/smoke.sh shiftbox-controller:development
```

The build exports and tests the static browser assets on the builder's native architecture, then embeds them in the cross-compiled Linux amd64 executable. The runtime image includes the pinned managed Dex helper and PostgreSQL 17 client tools. The image runs as UID and GID `10001`. Provide a persistent directory owned by this identity at `/var/lib/shiftbox`.

The default command is `shiftbox serve --config /var/lib/shiftbox/config.json`. To run another ShiftBox command in the image, supply its arguments as the container command. Supply the controller configuration and installation state at runtime.

Tag each release build with its release identifier. Publish the image through the release operator's registry session and use the registry digest when deploying it.
