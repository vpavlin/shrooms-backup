/**
 * withUnsignedRelease: a release build with no signature at all.
 *
 * Expo's template signs release builds with the well-known debug key, and the
 * publisher (jimmy-crib/publisher), which signs every APK with the app's own
 * key, refuses debug-signed input. So the release build is left unsigned and
 * handed over with its sha256; no key is ever on the building machine.
 */
const { withAppBuildGradle } = require("@expo/config-plugins");

module.exports = function withUnsignedRelease(config) {
  return withAppBuildGradle(config, (c) => {
    const g = c.modResults.contents;
    // The release buildType's `signingConfig signingConfigs.debug` line.
    const re = /(buildTypes\s*\{[\s\S]*?release\s*\{[\s\S]*?)signingConfig\s+signingConfigs\.debug/;
    if (!re.test(g)) throw new Error("withUnsignedRelease: no debug signingConfig in the release buildType to remove");
    c.modResults.contents = g.replace(re, "$1signingConfig null");
    return c;
  });
};
