package productruntime

// Never selects productionBuilders.
func Start(options any) (any, error) {
	return startWithBuilders(options, testBuilders())
}

func testBuilders() any                       { return nil }
func startWithBuilders(any, any) (any, error) { return nil, nil }
