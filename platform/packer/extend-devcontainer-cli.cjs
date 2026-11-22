"use strict";

// Extend the verified npm build of @devcontainers/cli 0.90.0. Pin the
// compiled bundle as well as the tarball so a new upstream bundle cannot
// silently move the resolver or the configuration read.
const { createHash } = require("node:crypto");
const { readFileSync, writeFileSync } = require("node:fs");

const expectedSHA = "50c06bf3775388aec724c0d68af1704b1cf9d35d663e493e79f38389939b93c6";

function extendBundle(bundle) {
  const original = readFileSync(bundle);
  if (createHash("sha256").update(original).digest("hex") !== expectedSHA) {
    throw new Error("unsupported Dev Container CLI bundle SHA-256");
  }

  let source = original.toString("utf8");
  const replace = (anchor, replacement) => {
    const position = source.indexOf(anchor);
    if (position < 0 || position !== source.lastIndexOf(anchor)) {
      throw new Error("Dev Container CLI extension requires a unique bundle anchor");
    }
    source = source.slice(0, position) + replacement + source.slice(position + anchor.length);
  };

  // Li reads the selected config. Preserve both stock document readers and
  // associate the exact Buffer digest with the config used by this resolver.
  replace("async function Li(e,A,t,i,r,n,o,s){let C=await VN(e).readDocument(s??t);if(!C)return;", String.raw`
var shiftboxConfigReceipts = new WeakMap();
async function Li(e,A,t,i,r,n,o,s){
  let C, shiftboxDigest;
  if (process.argv[2] === "up" && !s && (t.scheme === "file" || t.scheme === mn.scheme)) {
    try {
      const bytes = t.scheme === "file" ? await Ae(t.fsPath) : await e.readFile(Be(t,e.platform));
      shiftboxDigest = require("node:crypto").createHash("sha256").update(bytes).digest("hex");
      C = bytes.toString();
    } catch (error) {
      if (t.scheme === "file" && error.code !== "ENOENT") throw error;
    }
  } else {
    C = await VN(e).readDocument(s??t);
  }
  if (!C) return;`);
  replace("let Q=await VL(e,A,E,i,r,n,o),I=u=>Mo(",
    "if (shiftboxDigest) shiftboxConfigReceipts.set(E,shiftboxDigest);let Q=await VL(e,A,E,i,r,n,o),I=u=>Mo(");

  // PL runs after Feature policy and the initializer. Lp builds, LL reads
  // image metadata, zi merges metadata, and MQ applies normal UID adaptation.
  // Keep the stock spawn, mount, lifecycle, and result handling after this seam.
  const resolver = "async function PL(e,A,t,i,r){";
  replace(resolver, cacheResolver + "\n" + resolver);
  replace("let E=await Lp(e,A,r,!0);C=E.imageMetadata.config,a=zi(o,C);let{containerUser:Q}=a,I=await MQ(e,a,E.updatedImageName[0],E.imageDetails,TL(o.runArgs)||Q);",
    "const { build:E, metadata:shiftboxMetadata, merged:shiftboxMerged, containerUser:Q, imageName:I }=await shiftboxResolveImage(e,A,r);C=shiftboxMetadata;a=shiftboxMerged;");
  replace("ociAuthDiagnostics:i.common.ociAuthDiagnostics,finishBackgroundTasks:",
    "ociAuthDiagnostics:i.common.ociAuthDiagnostics,shiftboxImage:s.params.shiftboxImage,finishBackgroundTasks:");

  writeFileSync(bundle, source);
}

const cacheResolver = String.raw`
async function shiftboxResolveImage(params, config, additionalFeatures) {
  const { common } = params;
  const currentConfig = config.config;
  const configSHA256 = shiftboxConfigReceipts.get(config.raw);
  const cacheImage = process.env.SHIFTBOX_DEVCONTAINER_CACHE_IMAGE;
  const eligible = Xi(currentConfig) || Object.values({...currentConfig.features, ...additionalFeatures}).some(value => value !== false);
  if (eligible && configSHA256 && configSHA256 === process.env.SHIFTBOX_DEVCONTAINER_CACHE_CONFIG_SHA256 && shiftboxImageID(cacheImage)) {
    try {
      // Inspect only the local daemon. The guest has already pulled and
      // verified the authorized manifest and supplied its immutable image ID.
      const image = await vd(params, cacheImage);
      if (image.Id === cacheImage && image.Os === params.targetPlatformInfo.os && image.Architecture === params.targetPlatformInfo.arch) {
        // This label already contains the project's metadata. Parse and
        // substitute it for this checkout without appending another copy.
        const imageMetadata = LL(image, config.substitute, common.output);
        if (imageMetadata.raw.length && imageMetadata.raw.every(entry => entry && typeof entry === "object" && !Array.isArray(entry))) {
          const build = { updatedImageName: [cacheImage], imageMetadata, imageDetails: async () => image };
          const metadata = build.imageMetadata.config;
          const merged = zi(currentConfig, metadata);
          const containerUser = merged.containerUser;
          const imageName = await shiftboxCachedUID(params, merged, cacheImage, build.imageDetails, TL(currentConfig.runArgs) || containerUser);
          common.shiftboxImage = { configSHA256, imageCacheHit: true };
          return { build, metadata, merged, containerUser, imageName };
        }
      }
    } catch {
      common.output.write("ShiftBox cached devcontainer image was unavailable; building normally.", 3);
    }
  }

  const build = await Lp(params, config, additionalFeatures, true);
  const reference = build.updatedImageName[0];
  if (eligible && configSHA256 && (Xi(currentConfig) || reference !== currentConfig.image)) {
    try {
      const image = await vd(params, reference);
      if (shiftboxImageID(image.Id)) common.shiftboxImage = { builtImageID: image.Id, configSHA256, imageCacheHit: false };
    } catch {
      common.output.write("ShiftBox could not capture a local devcontainer build result.", 3);
    }
  }
  const metadata = build.imageMetadata.config;
  const merged = zi(currentConfig, metadata);
  const containerUser = merged.containerUser;
  const imageName = await MQ(params, merged, reference, build.imageDetails, TL(currentConfig.runArgs) || containerUser);
  return { build, metadata, merged, containerUser, imageName };
}

function shiftboxImageID(value) {
  return typeof value === "string" && /^sha256:[a-f0-9]{64}$/.test(value);
}

async function shiftboxCachedUID(params, merged, imageID, imageDetails, user) {
  if (!await RW(params, merged, imageID, imageDetails, user)) return imageID;

  // Docker can start the image by ID, but BuildKit needs a named FROM
  // reference for a requested UID adaptation. Tag the exact local image only
  // for that operation and immediately release the temporary reference.
  const tag = "shiftbox-local/devcontainer-cache:" + require("node:crypto").randomUUID();
  await qe(params, "tag", imageID, tag);
  try {
    if ((await vd(params, tag)).Id !== imageID) throw new Error("cached image tag changed");
    return await MQ(params, merged, tag, imageDetails, user);
  } finally {
    try {
      await qe(params, "image", "rm", tag);
    } catch {
      params.common.output.write("ShiftBox could not release a local UID adaptation tag.", 3);
    }
  }
}`;

try {
  if (process.argv.length !== 3) throw new Error("usage: node extend-devcontainer-cli.cjs <bundle>");
  extendBundle(process.argv[2]);
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
