package main

// version is set at build time via -X main.version=<tag>
var version = "dev"

func main() {
	_ = version
}
