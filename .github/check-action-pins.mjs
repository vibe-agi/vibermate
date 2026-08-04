import { readdir, readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const workflowDirectory = resolve(
  dirname(fileURLToPath(import.meta.url)),
  "workflows",
);
const remoteAction = /^[^@\s]+@[0-9a-f]{40}$/u;
const pinnedContainer = /^docker:\/\/[^\s]+@sha256:[0-9a-f]{64}$/u;
export function immutableActionReference(reference) {
  return (
    reference.startsWith("./") ||
    remoteAction.test(reference) ||
    pinnedContainer.test(reference)
  );
}

export async function checkActionPins(directory) {
  const failures = [];
  let references = 0;
  for (const file of (await readdir(directory)).sort()) {
    if (!/\.ya?ml$/u.test(file)) {
      continue;
    }
    const source = await readFile(resolve(directory, file), "utf8");
    for (const [index, line] of source.split(/\r?\n/u).entries()) {
      const match = line.match(/^\s*(?:-\s*)?uses:\s*([^\s#]+)/u);
      if (match === null) {
        if (/^\s*(?:-\s*)?uses:\s*(?:#.*)?$/u.test(line)) {
          failures.push(
            `${file}:${index + 1}: uses must be a single pinned scalar`,
          );
        }
        continue;
      }
      references += 1;
      const reference = match[1];
      if (!immutableActionReference(reference)) {
        failures.push(
          `${file}:${index + 1}: ${reference} is not pinned to an immutable digest`,
        );
      }
    }
  }
  if (references === 0) {
    failures.push("no GitHub Actions references were found");
  }
  if (failures.length !== 0) {
    throw new Error(failures.join("\n"));
  }
  return references;
}

if (
  process.argv[1] !== undefined &&
  import.meta.url === pathToFileURL(resolve(process.argv[1])).href
) {
  const references = await checkActionPins(workflowDirectory);
  console.log(`${references} GitHub Actions references are immutably pinned`);
}
