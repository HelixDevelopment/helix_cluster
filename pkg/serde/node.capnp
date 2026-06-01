using Go = import "/go.capnp";

@0xb3d4e5f6a7c8d9e0;

$Go.package("serde");
$Go.import("github.com/HelixDevelopment/helix_cluster/pkg/serde");

# Node represents a control-plane node in Helix Cluster OS.
struct Node {
  id       @0 :Text;    # unique node identifier (UUID)
  hostname @1 :Text;    # DNS hostname
  cores    @2 :UInt32;  # logical CPU count
  memBytes @3 :UInt64;  # total memory in bytes
  labels   @4 :List(Text);  # key=value label pairs
}
