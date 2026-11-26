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

Use the Go version in `go.mod` and the Node version in `ui/package.json`. The build exports and embeds the UI in the Go controller. See the repository's Makefile and fixture setup for integration and browser checks.

For a fresh AWS release test, use the checked-in CloudFormation template with verified release inputs. Choose private or public HTTPS in the same form. [AWS development workflow](https://github.com/jflatten/ShiftBox/tree/main/platform/development)

For templates with manual deployment phases, follow the [setup helper guide]({{ '/install/setup-helper/' | relative_url }}).

Branches and pull requests use normal CI. [ShiftBox architecture on main](https://github.com/jflatten/ShiftBox/blob/main/documentation/architecture/design-readback.md)
