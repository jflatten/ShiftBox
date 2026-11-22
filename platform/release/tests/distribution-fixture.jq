# Values come from local test artifacts. None identify an AWS or OCI release.
def digest: "sha256:" + ("a" * 64);
def regions: ["us-east-1", "us-east-2", "us-west-1", "us-west-2"];
{
  schemaVersion: 1,
  candidateTag: $tag,
  sourceCommit: $commit,
  runtimeCompatibility: $tag,
  publisherAccountId: $publisher,
  controller: {
    repository: "ghcr.io/jflatten/shiftbox",
    digest: digest,
    platform: "linux/amd64"
  },
  postgresImage: ("docker.io/library/postgres:17-bookworm@" + digest),
  template: {name: "shiftbox-aws-cloudformation.yaml", sha256: $template},
  installer: {name: "install.sh", sha256: $installer},
  supportAssets: {name: "shiftbox-aws-support.tar.gz", sha256: $support},
  regions: (
    reduce (regions | to_entries[]) as $entry ({};
      ($entry.value) as $region |
      ("0123456789abcdef" + (($entry.key + 1) | tostring)) as $id |
      "2026+rc/one=" as $version |
      "fixture-templates-\($region)" as $bucket |
      "public/templates/\($tag)/\($template)/shiftbox-aws-cloudformation.yaml" as $key |
      "https://\($bucket).s3.\($region).amazonaws.com/\($key)?versionId=\($version | @uri)" as $url |
      .[$region] = {
        hostAmiId: ("ami-" + $id),
        workspaceImage: {
          amiId: ("ami-" + ($id | explode | reverse | implode)),
          rootDeviceName: "/dev/sda1",
          rootSnapshotId: ("snap-" + $id),
          sourceAmiId: "ami-00000000000000000",
          sourceRegion: "us-east-2",
          sourceCommit: $commit,
          guestRelease: $tag,
          architecture: "amd64",
          minimumDiskGiB: 32,
          catalogPath: "catalog/\($region).json",
          catalogSha256: $catalog[$region]
        },
        templateLocation: {
          bucket: $bucket,
          key: $key,
          versionId: $version,
          url: $url,
          quickCreateUrl: "https://\($region).console.aws.amazon.com/cloudformation/home?region=\($region)#/stacks/create/review?templateURL=\($url | @uri)",
          sha256: $template
        }
      })
  )
}
