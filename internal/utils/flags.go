package utils

import (
	"flag"
	"fmt"
	"os"
)

var (
	Dir  = flag.String("dir", "data", "Path to the directory")
	Port = flag.Int("port", 8080, "Port number")
	Help = flag.Bool("help", false, "information")
)

func HelpFlag() string {
	return `
Simple Storage Service.

**Usage:**
	triple-s [-port <N>] [-dir <S>]  
	triple-s --help

**Options:**
	--help     Show this screen.
	--port N   Port number
	--dir S    Path to the directory
	`
}

func Checkflag() {
	err := flag.CommandLine.Parse(os.Args[1:])
	if err != nil {
		fmt.Println("Error:", err)
		fmt.Println(HelpFlag())
		os.Exit(1)
	}

	for _, arg := range flag.Args() {
		fmt.Printf("Unknown argument: %s\n", arg)
		fmt.Println(HelpFlag())
		os.Exit(1)
	}
	if *Help {
		fmt.Println(HelpFlag())
		os.Exit(0)
	}

	if _, err := os.Stat(*Dir); os.IsNotExist(err) {
		data := "data"
		os.Mkdir(data, 0o777)
	}

	if *Port < 1024 || *Port > 49151 {
		fmt.Println("Port should be 1024-49151")
		os.Exit(1)
	}
}
