package productruntime

// Start selects a test builder.
func Start(options any) (any, error) {
	return startWithBuilders(options, testBuilders())
}

// A decoy elsewhere in the package.
func unused() any { return productionBuilders() }

func testBuilders() any                       { return nil }
func productionBuilders() any                 { return nil }
func startWithBuilders(any, any) (any, error) { return nil, nil }
