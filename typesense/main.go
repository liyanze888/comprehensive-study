package main

import "comprehensive-study/typesense/codes"

func main() {
	worker := codes.NewTypeSenseWorker("http://localhost:8109", "xyz")
	codes.NewUserCharacterInteraction(worker).DoWork()
}
