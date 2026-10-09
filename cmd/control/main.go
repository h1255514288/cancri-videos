package main

import (
 "os"
 "github.com/yourorg/video-distribution-go/internal/shared/server"
)

func main() { os.Exit(server.Main("control")) }
