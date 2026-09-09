// Consistency checks for the extension.
//
//   node check.mjs
//
// These exist because `node --check` only validates syntax. A message type
// sent by a popup with no matching case in the worker parses perfectly and
// fails at runtime — which is exactly how three handlers were once deleted
// by an editing mistake and went unnoticed.

import { readFileSync } from "node:fs";

const read = (f) => readFileSync(new URL(f, import.meta.url), "utf8");
const worker = read("./background.js");
const senders = { "popup.js": read("./popup.js"), "content.js": read("./content.js") };
const manifest = JSON.parse(read("./manifest.json"));

let failures = 0;
const fail = (msg) => {
  console.error(`FAIL ${msg}`);
  failures++;
};

// 1. Every message type any surface sends must have a case in the worker.
const handled = new Set([...worker.matchAll(/case "([A-Z_]+)":/g)].map((m) => m[1]));
const sent = new Map();
for (const [file, src] of Object.entries(senders)) {
  for (const m of src.matchAll(/type:\s*"([A-Z_]+)"/g)) {
    if (!sent.has(m[1])) sent.set(m[1], file);
  }
}
for (const [type, file] of sent) {
  if (!handled.has(type)) fail(`${file} sends "${type}" but background.js has no case for it`);
}
for (const type of handled) {
  if (!sent.has(type)) console.warn(`note: background.js handles "${type}" that nothing sends`);
}

// 2. Every helper method the worker calls must exist in the helper's API.
const api = readFileSync(new URL("../flow-notes-local/api.go", import.meta.url), "utf8");
const helperMethods = new Set([...api.matchAll(/=\s*"([a-z]+\.[a-z]+|health)"/g)].map((m) => m[1]));
for (const m of worker.matchAll(/callHelper\("([^"]+)"/g)) {
  if (!helperMethods.has(m[1])) fail(`background.js calls helper method "${m[1]}" which api.go does not define`);
}

// 3. Every element id the popup scripts touch must exist in popup.html.
const html = read("./popup.html");
for (const m of read("./popup.js").matchAll(/getElementById\("([^"]+)"\)/g)) {
  if (!html.includes(`id="${m[1]}"`)) fail(`popup.js uses #${m[1]} which popup.html does not define`);
}

// 4. Permissions must be declared AND used. A declared-but-unused
//    permission is a documented Chrome Web Store rejection reason, and
//    activeTab sat in the manifest unused until this check was written.
const permissionUse = {
  storage: /chrome\.storage\./,
  identity: /chrome\.identity\./,
  nativeMessaging: /sendNativeMessage|connectNative/,
  scripting: /chrome\.scripting\./,
  activeTab: /activeTab/,
  tabs: /chrome\.tabs\./,
  notifications: /chrome\.notifications\./,
  contextMenus: /chrome\.contextMenus\./,
  alarms: /chrome\.alarms\./,
};
const allCode = [worker, ...Object.values(senders)].join("\n");
for (const perm of manifest.permissions) {
  const pattern = permissionUse[perm];
  if (!pattern) {
    console.warn(`note: no usage check defined for permission "${perm}"`);
    continue;
  }
  if (!pattern.test(allCode)) {
    fail(`manifest.json declares "${perm}" but no code uses it — reviewers reject unused permissions`);
  }
}

// 5. Permissions the code actually relies on must be declared.
const needed = [
  ["nativeMessaging", /sendNativeMessage/],
  ["scripting", /chrome\.scripting\./],
  ["storage", /chrome\.storage\./],
  ["identity", /chrome\.identity\./],
];
const declared = new Set(manifest.permissions);
for (const [perm, used] of needed) {
  if (used.test(worker) && !declared.has(perm)) fail(`code uses ${perm} but manifest.json does not declare it`);
}

console.log(
  failures ? `\n${failures} check(s) failed` : `All checks passed (${sent.size} message types, ${helperMethods.size} helper methods)`
);
process.exit(failures ? 1 : 0);
