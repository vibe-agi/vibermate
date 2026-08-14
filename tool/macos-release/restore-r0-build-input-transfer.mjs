import { restoreR0BuildInputTransfer } from "./r0-build-input-transfer.mjs";

if (process.argv.length !== 2) {
  throw new Error(
    "The source-traceability build-input transfer (R0) restorer accepts no arguments",
  );
}

const result = await restoreR0BuildInputTransfer(process.env);
process.stdout.write(
  `archiveSHA256=${result.archiveSHA256}\n` +
    `inputTreeSHA256=${result.inputTreeSHA256}\n`,
);
