# Resolve immutable artifacts from the exact workflow run returned by the API.
if type != "array" or length == 0 or any(.[]; (.artifacts | type) != "array") then
  error("invalid workflow artifact pages")
else
  map(.artifacts) | add
end |
. as $artifacts |
def id($prefix):
  [ $artifacts[] | select(.name == ($prefix + $tag)) ] |
  if length == 0 then ""
  elif length == 1 and .[0].expired == false and
    (.[0].id | type == "number" and . > 0 and floor == .) then (.[0].id | tostring)
  else error("ambiguous, expired or invalid candidate handoff: " + $prefix + $tag)
  end;
id("release-assets-") as $assets |
if $assets != "" then
  "build=", "copy=", "share=", "assets=" + $assets
else
  id("ami-build-") as $build |
  id("ami-copy-") as $copy |
  id("ami-share-") as $share |
  if ($copy != "" and $build == "") or ($share != "" and $copy == "") then
    error("candidate handoff is missing its predecessor")
  else
    "build=" + $build, "copy=" + $copy, "share=" + $share, "assets="
  end
end
