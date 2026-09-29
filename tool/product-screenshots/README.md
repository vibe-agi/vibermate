# Product screenshots

Renders the screenshots used by the README and the product page on
vibe-agi.github.io. Every image is taken from the Preview dataset, never from
real traffic, in English and Chinese at 2400 and 1280 pixels wide.

```sh
cd ui/flutter_app
tool/build_web.sh \
  --dart-define=VIBERMATE_PREVIEW=true \
  --dart-define=VIBERMATE_PREVIEW_SERVER=true \
  --dart-define=VIBERMATE_SCREENSHOT=true \
  -o ../../dist/preview-web
cd ../../tool/product-screenshots
npm ci
npx playwright install chromium
node capture.mjs ../../dist/preview-web ../../dist/product-screenshots
```

- `VIBERMATE_PREVIEW_SERVER` previews the Runtime Server Owner workbench, which
  includes Runtime Users and usage reports.
- `VIBERMATE_SCREENSHOT` hides the Preview markers; pages that publish these
  images state that they show sample data.

Copy `dist/product-screenshots/*.webp` to `public/images/vibermate/` in the
vibe-agi.github.io repository.
