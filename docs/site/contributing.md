---
layout: default
title: Build and contribute
permalink: /contributing/
---

# Build ShiftBox from source

Clone the public repository and work from reviewed `main`.

```sh
git clone https://github.com/jflatten/ShiftBox.git
cd ShiftBox
git switch main
npm --prefix ui ci
make fmt-check vet test race
make ui-test build
```

Use the Go version in `go.mod` and the Node release family in `ui/package.json`. The build exports and embeds the UI in the Go controller. See the repository's Makefile and fixture setup for integration and browser checks.

For AWS development, render the current source against the published image pins. The development template uses existing subnets and supports first-run setup over private HTTPS. [AWS development workflow](https://github.com/jflatten/ShiftBox/tree/main/platform/development)

Branches and pull requests use normal CI. Protected version tags control prebuilt public RC and stable packages. See the repository's [contributing and architecture sources](https://github.com/jflatten/ShiftBox) and [official releases](https://github.com/jflatten/ShiftBox/releases).
