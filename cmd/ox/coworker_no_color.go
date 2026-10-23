package main

import "os"

// coworkerNoColor is NO_COLOR as the coworker's environment set it.
//
// main.go's init sets NO_COLOR=1 whenever stdout is not a terminal or an AI
// coworker is detected, so by the time a command runs, os.Getenv("NO_COLOR")
// no longer says whether the coworker opted out. Package-level variables are
// initialized before any init function runs, so this still holds the
// original value. Pass it to oxactivity.ColorAllowedFor.
var coworkerNoColor = os.Getenv("NO_COLOR")
