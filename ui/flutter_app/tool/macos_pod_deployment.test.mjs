import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { cp, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

test("Podfile raises old or absent deployment floors to 14 and preserves higher declared floors", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "vibermate-pod-floor-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await mkdir(join(root, "Flutter/ephemeral"), { recursive: true });
  await writeFile(join(root, "Flutter/ephemeral/Flutter-Generated.xcconfig"), `FLUTTER_ROOT=${root}\n`);
  await cp(new URL("../macos/Podfile", import.meta.url), join(root, "Podfile"));
  const ruby = String.raw`
require 'json'
require 'rubygems'
module Kernel
  alias original_require require
  def require(path)
    path.end_with?('/bin/podhelper') ? true : original_require(path)
  end
end
def platform(name, value); $platform = value; end
def project(*args); end
def target(*args); yield; end
def use_frameworks!; end
def inherit!(*args); end
def flutter_macos_podfile_setup; end
def flutter_install_all_macos_pods(*args); end
def flutter_additional_macos_build_settings(target)
  target.build_configurations.each { |config| config.build_settings['FLUTTER_HOOK'] = 'preserved' }
end
def post_install
  configs = ['10.15', nil, '14.0', '15.1'].map { |version| Struct.new(:build_settings).new({'MACOSX_DEPLOYMENT_TARGET' => version}) }
  target = Struct.new(:build_configurations).new(configs)
  installer = Struct.new(:pods_project).new(Struct.new(:targets).new([target]))
  yield installer
  $settings = configs.map(&:build_settings)
end
load ARGV.fetch(0)
puts JSON.generate({platform: $platform, settings: $settings})
`;
  const result = spawnSync("/usr/bin/ruby", ["-e", ruby, join(root, "Podfile")], { encoding: "utf8", timeout: 10_000 });
  assert.equal(result.status, 0, result.stderr);
  const observed = JSON.parse(result.stdout);
  assert.deepEqual(observed, {
    platform: "14.0",
    settings: ["14.0", "14.0", "14.0", "15.1"].map((floor) => ({ MACOSX_DEPLOYMENT_TARGET: floor, FLUTTER_HOOK: "preserved" })),
  });
});
