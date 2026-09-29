{{flutter_js}}
{{flutter_build_config}}

// The workbench is self-contained: CanvasKit ships in the build
// (--no-web-resources-cdn) and the engine's fallback fonts are served from
// fonts/, verified against tool/web_fallback_fonts.json. Nothing loads from a
// third-party origin.
_flutter.loader.load({
  config: {
    fontFallbackBaseUrl: "fonts/",
  },
});
