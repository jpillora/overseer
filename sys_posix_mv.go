package overseer

// TODO(@jpillora) borrowed from https://github.com/aisola/go-coreutils/blob/master/mv/mv.go
//
// mv.go (go-coreutils) 0.1
// Copyright (C) 2014, The GO-Coreutils Developers.
//
// Written By: Abram C. Isola, Michael Murphy
//
// package main
//
// import "bufio"
// import "flag"
// import "fmt"
// import "io"
// import "os"
// import "path/filepath"
//
// const (
// 	help_text string = `
//     Usage: mv [OPTION]... [PATH]... [PATH]
//        or: mv [PATH] [PATH]
//        or: mv [OPTION]
//     move or rename files or directories
//         --help        display this help and exit
//         --version     output version information and exit
//         -f, --force   remove existing destination files and never prompt the user
//     ` // -v, --verbose print the name of each file before moving it
// 	version_text = `
//     mv (go-coreutils) 0.1
//     Copyright (C) 2014, The GO-Coreutils Developers.
//     This program comes with ABSOLUTELY NO WARRANTY; for details see
//     LICENSE. This is free software, and you are welcome to redistribute
//     it under certain conditions in LICENSE.
// `
// )
//
// var (
// 	forceEnabled     = flag.Bool("f", false, "remove existing destination files and never prompt the user")
// 	forceEnabledLong = flag.Bool("force", false, "remove existing destination files and never prompt the user")
// )
//
// // The input function prints a statement to the user and accepts an input, then returns the input.
//
// func input(prompt, location string) string {
// 	fmt.Printf(prompt, location)
//
// 	reader := bufio.NewReader(os.Stdin)
// 	userinput, _ := reader.ReadString([]byte("\n")[0])
//
// 	return userinput
// }
//
// // The fileExists function will check if the file exists.
//
// func fileExists(filep string) os.FileInfo {
// 	fp, err := os.Stat(filep)
// 	if err != nil && os.IsNotExist(err) {
// 		return nil
// 	}
// 	return fp
// }
//
// /* The argumentCheck function will check the number of arguments given to the program and process them
//  * accordingly. */
//
// func argumentCheck(files []string) {
// 	switch len(files) {
// 	case 0: // If there is no argument
// 		fmt.Println("mv: missing file operand\nTry 'mv -help' for more information")
// 		os.Exit(0)
// 	case 1: // If there is one argument
// 		fmt.Printf("mv: missing destination file operand after '%s'\nTry 'mv -help' for more information.\n", files[0])
// 		os.Exit(0)
// 	case 2: // If there are two arguments
// 		mover(files[0], files[1])
// 	default: // If there are more than two arguments
// 		to_file, files := files[len(files)-1], files[:len(files)-1]
//
// 		if fp := fileExists(to_file); fp == nil || !fp.IsDir() {
// 			fmt.Println("mv: when moving multiple files, last argument must be a directory")
// 			os.Exit(0)
// 		} else {
// 			fmt.Println(files)
// 			for i := 0; i < len(files); i++ {
// 				mover(files[i], to_file)
// 			}
// 			os.Exit(0)
// 		}
// 	}
// }
//
// /* The mover function will take two strings as an argument and move the original file/dir to
//  * a new location. */
//
// func mover(originalLocation, newLocation string) {
// 	fp := fileExists(newLocation)
//
// 	switch {
// 	case fileExists(originalLocation) == nil: // If the original file does not exist
// 		fmt.Printf("mv: cannot stat '%s': No such file or directory\n", originalLocation)
// 		os.Exit(0)
// 	case fp != nil && !*forceEnabled: // If the destination file does not exist and forceEnabled is disabled
// 		if fp.IsDir() {
// 			base := filepath.Base(originalLocation)
// 			if fp2 := fileExists(newLocation + "/" + base); fp2 != nil && !*forceEnabled {
// 				answer := input("File '%s' exists. Overwrite? (y/N): ", newLocation+"/"+base)
// 				if answer == "y\n" {
// 					try_move(originalLocation, newLocation+"/"+base)
// 				} else {
// 					os.Exit(0)
// 				}
// 			} else if fp2 != nil && *forceEnabled {
// 				try_move(originalLocation, newLocation+"/"+base)
// 			} else if fp2 == nil {
// 				try_move(originalLocation, newLocation+"/"+base)
// 			}
// 		} else {
// 			answer := input("File '%s' exists. Overwrite? (y/N): ", newLocation)
// 			if answer == "y\n" {
// 				try_move(originalLocation, newLocation)
// 			} else {
// 				os.Exit(0)
// 			}
// 		}
// 	default: // If the destination file exists and forceEnabled is enabled,
// 		try_move(originalLocation, newLocation) // or if the file does not exist, move it.
// 	}
// }
//
// func try_move(originalLocation, newLocation string) error {
// 	err := os.Rename(originalLocation, newLocation)
// 	switch t := err.(type) {
// 	case *os.LinkError:
// 		fmt.Printf("Cross-device move. Copying instead\n")
// 		return move_across_devices(originalLocation, newLocation)
// 	case *os.PathError:
// 		fmt.Printf("Path error: %q\n", t)
// 		return err
// 	case *os.SyscallError:
// 		fmt.Printf("Syscall error: %q\n", t)
// 		return err
// 	case nil:
// 		return nil
// 	default:
// 		fmt.Printf("Unkown error Type: %T Error: %q", t, t)
// 		return err
// 	}
// 	return nil
// }
//
// func move_across_devices(originalLocation, newLocation string) error {
// 	src, err := os.Open(originalLocation)
// 	if err != nil {
// 		return err
// 	}
// 	defer src.Close()
//
// 	dst, err := os.Create(newLocation)
// 	if err != nil {
// 		return err
// 	}
// 	defer dst.Close()
//
// 	size, err := io.Copy(dst, src)
// 	if err != nil {
// 		return err
// 	}
//
// 	srcStat, err := os.Stat(originalLocation)
// 	if err != nil {
// 		return err
// 	}
// 	if size != srcStat.Size() {
// 		os.Remove(newLocation)
// 		return fmt.Errorf("Error, file was not copied completely")
// 	}
// 	os.Remove(originalLocation)
// 	return nil
// }
//
// func main() {
// 	help := flag.Bool("help", false, help_text)
// 	version := flag.Bool("version", false, version_text)
// 	flag.Parse()
//
// 	// We only need one instance of forceEnabled
//
// 	if *forceEnabledLong {
// 		*forceEnabled = true
// 	}
//
// 	// Display help information
//
// 	if *help {
// 		fmt.Println(help_text)
// 		os.Exit(0)
// 	}
//
// 	// Display version information
//
// 	if *version {
// 		fmt.Println(version_text)
// 		os.Exit(0)
// 	}
//
// 	files := flag.Args() // Obtain a list of files.
// 	argumentCheck(files) // Check the number of arguments and process them.
// }
