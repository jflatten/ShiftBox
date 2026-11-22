# Distribution integration

ShiftBox embeds CNCF Distribution v3.1.2 from release commit
`3220848f15d9279c66a41aa0a257469e6aede1e9`. The Go module sums pin the
source and module manifest. The linked library retains its Apache 2.0 license.
The upstream release has no root NOTICE. The linked dependency inventory and
applicable notices are verified against the release binary in T021–T022.

The stock proxy discovers authentication with default HTTP clients, can
fall back to stale tags after any upstream error, and writes through storage
without ShiftBox admission. The adaptation uses Distribution's registry
middleware seam and the existing OCI handler. The transport, origin
namespace, write admission and stale decisions belong to ShiftBox. Keep
upstream transport and auth behavior confined to the embedded cache.
