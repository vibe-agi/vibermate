# Keep Web manual proxy authority on the Runtime Server

Status: proposed until the first-phase Web acceptance gates pass.

A Server Owner's Web Session may create, rotate, and revoke Manual Proxy Logins through the Runtime Server's existing Manual Capture interface; other Web users and read-only capabilities cannot. The returned proxy address is the configured client-reachable Server Access Address, not a bind address or server-local relay, and a remote browser receives a fingerprint-bound download of the public Proxy CA instead of a path on the Server filesystem. The Server HTTPS Identity still authenticates the outer proxy connection independently of intercepted AI hosts, including when a private deployment happens to use the same CA as issuer. We rejected exposing the existing local-file contract remotely: it would make a successful-looking login that cannot be used by its client and could disclose deployment paths.
