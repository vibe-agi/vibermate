package productruntime

// StorageLocation describes this Runtime's resolved persistent directory, not
// a browser's filesystem or a guessed platform default. It contains no secret
// material and is exposed only through authenticated owner management.
type StorageLocation struct {
	Backend       string `json:"backend"`
	DataDirectory string `json:"dataDirectory"`
	DatabasePath  string `json:"databasePath"`
}

func (r *Runtime) StorageLocation() StorageLocation {
	return StorageLocation{
		Backend: "sqlite", DataDirectory: r.paths.DataDirectory(),
		DatabasePath: r.paths.DatabasePath(),
	}
}
