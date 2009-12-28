// Copyright 2009 by Maurice Gilden. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
 gobuild - build tool to automate building go programs
*/
package main

import (
	os "os";
	"exec";
	"flag";
	"path";
	"strings";
	"./logger";
	"./godata";
)

// ========== command line parameters ==========

var flagLibrary *bool = flag.Bool("lib", false, "build all packages as librarys");
var flagBuildAll *bool = flag.Bool("a", false, "build all executables");
var flagTesting *bool = flag.Bool("t", false, "(not yet implemented) Build all tests");
var flagSingleMainFile *bool = flag.Bool("single-main", false, "one main file per executable");
var flagIncludeInvisible *bool = flag.Bool("include-hidden", false, "Include hidden directories");
var flagOutputFileName *string = flag.String("o", "", "output file");
var flagQuietMode *bool = flag.Bool("q", false, "only print warnings/errors");
var flagQuieterMode *bool = flag.Bool("qq", false, "only print errors");
var flagVerboseMode *bool = flag.Bool("v", false, "print debug messages");
var flagIncludePaths *string = flag.String("I", "", "additional include paths");
var flagClean *bool = flag.Bool("clean", false, "delete all temporary files");

// ========== global (package) variables ==========

var compilerBin string;
var linkerBin string;
var gopackBin string = "gopack";
var compileError bool = false;
var linkError bool = false;
var rootPath string;
var rootPathPerm int;
var objExt string;
var outputDirPrefix string;
var goPackages *godata.GoPackageContainer;

// ========== goFileVisitor ==========

// this visitor looks for files with the extension .go
type goFileVisitor struct {}

	
// implementation of the Visitor interface for the file walker
func (v *goFileVisitor) VisitDir(path string, d *os.Dir) bool {
	if path[strings.LastIndex(path, "/") + 1] == '.' {
		return *flagIncludeInvisible;
	}
	return true;
}

func (v *goFileVisitor) VisitFile(path string, d *os.Dir) {
	// parse hidden directories?
	if (path[strings.LastIndex(path, "/") + 1] == '.') && (!*flagIncludeInvisible) {
		return;
	}

	if strings.HasSuffix(path, ".go") {
		// include _test.go files?
		if strings.HasSuffix(path, "_test.go") && (!*flagTesting) {
			return;
		}

		gf := godata.GoFile{path[len(rootPath)+1:len(path)], nil, false, false};
		gf.ParseFile(goPackages);
	}
}

// ========== (local) functions ==========

/*
 readFiles reads all files with the .go extension and creates their AST.
 It also creates a list of local imports (everything starting with ./)
 and searches the main package files for the main function.
*/
func readFiles(rootpath string) {
	// path walker error channel
	errorChannel := make(chan os.Error, 64);

	// visitor for the path walker
	visitor := &goFileVisitor{};
	
	logger.Info("Parsing go file(s)...\n");
	
	path.Walk(rootpath, visitor, errorChannel);
	
	if err, ok := <-errorChannel; ok {
		logger.Error("Error while traversing directories: %s\n", err);
	}
}

/*
 The compile method will run the compiler for every package it has found,
 starting with the main package.
*/
func compile(pack *godata.GoPackage) {
	var argv []string;
	var argvFilled int;

	// check for recursive dependencies
	if pack.InProgress {
		logger.Error("Found a recurisve dependency in %s. This is not supported in Go, aborting compilation.\n", pack.Name);
		os.Exit(1);
	}
	pack.InProgress = true;

	// first compile all dependencies
	pack.Depends.Do(func(e interface{}) {
		dep := e.(*godata.GoPackage);
		if !dep.Compiled {
			compile(dep);
		}
	});

	// check if this package has any files (if not -> error)
	if pack.Files.Len() == 0 {
		logger.Error("No files found for package %s.\n", pack.Name);
		os.Exit(1);
	}
	
	// construct compiler command line arguments
	if (pack.Name != "main") {
		logger.Info("Compiling %s...\n", pack.Name);
	} else {
		logger.Info("Compiling %s (%s)...\n", pack.Name, pack.OutputFile);
	}
	if *flagIncludePaths != "" {
		argv = make([]string, pack.Files.Len() + 5);
	} else {
		argv = make([]string, pack.Files.Len() + 3);
	}

	argv[argvFilled] = compilerBin; argvFilled++;
	argv[argvFilled] = "-o"; argvFilled++;
	argv[argvFilled] = outputDirPrefix + pack.OutputFile + objExt; argvFilled++;

	if *flagIncludePaths != "" {
		argv[argvFilled] = "-I"; argvFilled++;
		argv[argvFilled] = *flagIncludePaths; argvFilled++;
	}

	logger.Info("\tfiles: ");
	for i := 0; i < pack.Files.Len(); i++  {
		gf := pack.Files.At(i).(*godata.GoFile);
		argv[argvFilled] = gf.Filename;
		logger.Info("%s ", argv[argvFilled]);
		argvFilled++;
	}
	logger.Info("\n");
		
	cmd, err := exec.Run(compilerBin, argv[0:argvFilled], os.Environ(), exec.DevNull, 
		exec.PassThrough, exec.PassThrough);
	if err != nil {
		logger.Error("%s\n", err);
		os.Exit(1);
	}

	waitmsg, err := cmd.Wait(0);
	if err != nil {
		logger.Error("Compiler execution error (%s), aborting compilation.\n", err);
		os.Exit(1);
	}

	if waitmsg.ExitStatus() != 0 {
		compileError = true;
		pack.HasErrors = true;
	}
	
	// it should now be compiled
	pack.Compiled = true;
	pack.InProgress = false;

}

/*
 Calls the linker for the main file, which should be called "main.(5|6|8)".
*/
func link(pack *godata.GoPackage) {
	var argv []string;

	if *flagIncludePaths != "" {
		argv = make([]string, 6);
		argv = []string{
			linkerBin,
			"-o",
			outputDirPrefix + pack.OutputFile,
			"-L",
			*flagIncludePaths,
			outputDirPrefix + pack.OutputFile + objExt};
		
	} else {
		argv = make([]string, 4);
		argv = []string{
			linkerBin,
			"-o",
			outputDirPrefix + pack.OutputFile,
			outputDirPrefix + pack.OutputFile + objExt};

	}
	
	logger.Info("Linking %s...\n", argv[2]);

	cmd, err := exec.Run(linkerBin, argv, os.Environ(),
		exec.DevNull, exec.PassThrough, exec.PassThrough);
	if err != nil {
		logger.Error("%s\n", err);
		os.Exit(1);
	}
	waitmsg, err := cmd.Wait(0);
	if err != nil {
		logger.Error("Linker execution error (%s), aborting compilation.\n", err);
		os.Exit(1);
	}

	if waitmsg.ExitStatus() != 0 {
		logger.Error("Linker returned with errors, aborting.\n");
		os.Exit(1);
	}
}

func packLib(pack *godata.GoPackage) {

	logger.Info("Creating %s.a...\n", pack.Name);

	argv := []string{
		gopackBin,
		"crg", // create new go archive
		outputDirPrefix + pack.Name + ".a",
		outputDirPrefix + pack.Name + objExt};

	cmd, err := exec.Run(gopackBin, argv, os.Environ(),
		exec.DevNull, exec.PassThrough, exec.PassThrough);
	if err != nil {
		logger.Error("%s\n", err);
		os.Exit(1);
	}
	waitmsg, err := cmd.Wait(0);
	if err != nil {
		logger.Error("gopack execution error (%s), aborting.\n", err);
		os.Exit(1);
	}

	if waitmsg.ExitStatus() != 0 {
		logger.Error("gopack returned with errors, aborting.\n");
		os.Exit(1);
	}

}

/*
 Build an executable from the given sources.
*/
func buildExecutable() {
	// check if there's a main package:
	if goPackages.GetMainCount() == 0 {
		logger.Error("No main package found.\n");
		os.Exit(1);
	}

	// multiple main, no command file from command line and no -a -> error
	if (goPackages.GetMainCount() > 1) && (flag.NArg() == 0) && !*flagBuildAll {
		logger.Error("Multiple files found with main function.\n");
		logger.ErrorContinue("Please specify one or more as command line parameter or\n");
		logger.ErrorContinue("run gobuild with -a. Available main files are:\n");
		for _, fn := range goPackages.GetMainFilenames() {
			logger.ErrorContinue("\t %s\n", fn);
		}
		os.Exit(1);
	}
	
	// compile all needed packages
	if flag.NArg() > 0 {
		for _, fn := range flag.Args() {
			mainPack, exists := goPackages.GetMain(fn, !*flagSingleMainFile);
			if !exists {
				logger.Error("File %s not found.\n", fn);
				return; // or os.Exit?
			}

			compile(mainPack);

			// link everything together
			if !compileError {
				link(mainPack);
			} else {
				logger.Error("Can't link executable because of compile errors.\n");
			}
		}
	} else {
		for _, mainPack := range goPackages.GetMainPackages(!*flagSingleMainFile) {

			compile(mainPack);

			// link everything together
			if !compileError {
				link(mainPack);
			} else {
				logger.Error("Can't link executable because of compile errors.\n");
			}
		}
	}
	

}


/*
 Build library files (.a) for all packages or the ones given though
 command line parameters.
*/
func buildLibrary() {
	var packNames []string;
	var pack *godata.GoPackage;
	var exists bool;

	if goPackages.GetPackageCount() == 0 {
		logger.Warn("No packages found to build.\n");
		return;
	}

	// check for command line parameters
	if flag.NArg() > 0 {
		packNames = flag.Args();
	} else {
		packNames = goPackages.GetPackageNames();
	}


	// loop over all packages, compile them and build a .a file
	for _, name := range packNames {

		if name == "main" {
			continue; // don't make this into a library
		}
		
		pack, exists = goPackages.Get(name);
		if !exists {
			logger.Error("Package %s doesn't exist.\n", name);
			continue; // or exit?
		}
		
		// these packages come from invalid/unhandled imports
		if pack.Files.Len() == 0 {
			logger.Debug("Skipping package %s, no files to compile.\n", pack.Name);
			continue;
		}

		if !pack.Compiled {
			logger.Debug("Building %s...\n", pack.Name);
			compile(pack);
			packLib(pack);
		}
	}

}

/*
 This function does exactly the same as "make clean".
*/
func clean() {
	bashBin, err := exec.LookPath("bash");
	if err != nil {
		logger.Error("Need bash to clean.\n");
		os.Exit(1);
	}

	argv := []string{bashBin, "-c", "commandhere"};

	if *flagVerboseMode {
		argv[2] = "rm -rfv *.[568]";
	} else {
		argv[2] = "rm -rf *.[568]";
	}
	
	logger.Info("Running: %v\n", argv[2:]);

	cmd, err := exec.Run(bashBin, argv, os.Environ(),
		exec.DevNull, exec.PassThrough, exec.PassThrough);
	if err != nil {
		logger.Error("%s\n", err);
		os.Exit(1);
	}
	waitmsg, err := cmd.Wait(0);
	if err != nil {
		logger.Error("Couldn't delete files: %s\n", err);
		os.Exit(1);
	}

	if waitmsg.ExitStatus() != 0 {
		logger.Error("rm returned with errors.\n");
		os.Exit(1);
	}


}


// Returns the bigger number.
func max(a, b int) int {
	if a > b {
		return a;
	}
	return b;
}

func main() {
	var err os.Error;
	var rootPathDir *os.Dir;

	// parse command line arguments
	flag.Parse();

	if *flagQuieterMode {
		logger.SetVerbosityLevel(logger.ERROR);
	} else if *flagQuietMode {
		logger.SetVerbosityLevel(logger.WARN);
	} else if *flagVerboseMode {
		logger.SetVerbosityLevel(logger.DEBUG);
	}

	if *flagClean {
		clean();
		os.Exit(0);
	}
	
	// get the compiler/linker executable
	switch os.Getenv("GOARCH") {
	case "amd64":
		compilerBin = "6g";
		linkerBin = "6l";
		objExt = ".6";
	case "386":
		compilerBin = "8g";
		linkerBin = "8l";
		objExt = ".8";
	case "arm":
		compilerBin = "5g";
		linkerBin = "5l";
		objExt = ".5";
	default:
		logger.Error("Please specify a valid GOARCH (amd64/386/arm).\n");
		os.Exit(1);		
	}

	// get the complete path to the compiler/linker
	compilerBin, err = exec.LookPath(compilerBin);
	if err != nil {
		logger.Error("Could not find compiler %s: %s\n", compilerBin, err);
		os.Exit(1);
	}
	linkerBin, err = exec.LookPath(linkerBin);
	if err != nil {
		logger.Error("Could not find linker %s: %s\n", linkerBin, err);
		os.Exit(1);
	}
	gopackBin, err = exec.LookPath(gopackBin);
	if err != nil {
		logger.Error("Could not find gopack executable (%s): %s\n", gopackBin, err);
		os.Exit(1);
	}
	
	// get the root path from where the application was called
	// and its permissions (used for subdirectories)
	if rootPath, err = os.Getwd(); err != nil {
		logger.Error("Could not get the root path: %s\n", err);
		os.Exit(1);
	}
	if rootPathDir, err = os.Stat(rootPath); err != nil {
		logger.Error("Could not read the root path: %s\n", err);
		os.Exit(1);
	}
	rootPathPerm = rootPathDir.Permission();

	// create the package container
	goPackages = godata.NewGoPackageContainer();

	// check if -o with path
	if *flagOutputFileName != "" {
		dir, err := os.Stat(*flagOutputFileName);
		if err != nil {
			// doesn't exist? try to make it if it's a path
			if (*flagOutputFileName)[len(*flagOutputFileName)-1] == '/' {
				err = os.MkdirAll(*flagOutputFileName, rootPathPerm);
				if err == nil {
					outputDirPrefix = *flagOutputFileName;
				}
			} else {
				godata.DefaultOutputFileName = *flagOutputFileName;
			}
		} else if dir.IsDirectory() {
			if (*flagOutputFileName)[len(*flagOutputFileName)-1] == '/' {
				outputDirPrefix = *flagOutputFileName;
			} else {
				outputDirPrefix = *flagOutputFileName + "/";
			}
		} else {
			godata.DefaultOutputFileName = *flagOutputFileName;
		}

		// make path to output file
		if outputDirPrefix == "" && strings.Index(*flagOutputFileName, "/") != -1 {
			err = os.MkdirAll((*flagOutputFileName)[0:strings.LastIndex(*flagOutputFileName, "/")], rootPathPerm);
			if err != nil {
				logger.Error("Could not create %s: %s\n",
					(*flagOutputFileName)[0:strings.LastIndex(*flagOutputFileName, "/")],
					err);
			}
		}

	}

	// read all go files in the current path + subdirectories and parse them
	readFiles(rootPath);

	if *flagLibrary {
		buildLibrary();
	} else {
		buildExecutable();
	}
}
