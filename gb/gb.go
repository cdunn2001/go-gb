/* 
   Copyright 2011 John Asmuth

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

//target:gb
package main

import (
	"strings"
	"bufio"
	"os"
	"fmt"
	"path"
	"exec"
	//"gonicetrace.googlecode.com/hg/nicetrace"
)

var MakeCMD, CompileCMD, LinkCMD, PackCMD, CopyCMD, GoInstallCMD, GoFMTCMD string

var Install, Clean, Scan, ScanList, Test, Exclusive,
	GoInstall, Concurrent, Verbose, GenMake, Build,
	Force, Makefiles, GoFMT bool
var IncludeDir string
var Recurse bool
//var CWD string
var GCArgs []string
var GLArgs []string
var PackagesBuilt int
var PackagesInstalled int
var BrokenPackages int
var ListedTargets int
var ListedDirs map[string]bool

var Packages = make(map[string]*Package)

var GOROOT, GOOS, GOARCH string

func GetBuildDirPkg() (dir string) {
	return "_obj"
}

func GetInstallDirPkg() (dir string) {
	return path.Join(GOROOT, "pkg", GOOS+"_"+GOARCH)
}

func GetBuildDirCmd() (dir string) {
	return "."
}

func GetInstallDirCmd() (dir string) {
	return path.Join(GOROOT, "bin")
}


func GetSubDirs(dir string) (subdirs []string) {
	file, err := os.Open(dir, os.O_RDONLY, 0)
	if err != nil {
		return
	}
	infos, err := file.Readdir(-1)
	if err != nil {
		return
	}
	for _, info := range infos {
		if info.IsDirectory() {
			subdirs = append(subdirs, info.Name)
		}
	}
	return
}

func ScanDirectory(base, dir string) (err2 os.Error) {
	pkg, err := ReadPackage(base, dir)

	if err == nil {
		if Scan {
			if pkg.IsCmd {
				fmt.Printf("in %s: cmd \"%s\"\n", pkg.Dir, pkg.Target)
			} else {
				fmt.Printf("in %s: pkg \"%s\"\n", pkg.Dir, pkg.Target)
			}
			if ScanList {
				fmt.Printf(" Deps: %v\n", pkg.Deps)
				fmt.Printf(" TestDeps: %v\n", pkg.TestDeps)
			}
		}
		Packages["\""+pkg.Target+"\""] = pkg
		base = pkg.Base
	} else {
		tpath := path.Join(dir, "target.gb")
		fin, err := os.Open(tpath, os.O_RDONLY, 0)
		if err == nil {
			bfrd := bufio.NewReader(fin)
			base, err = bfrd.ReadString('\n')
			base = strings.TrimSpace(base)
		}
	}
	
	if pkg.Target == "." {
		return os.NewError("Package has no name specified. Either create 'target.gb' or run gb from above.")
	}
	
	if Recurse {
		subdirs := GetSubDirs(dir)
		for _, subdir := range subdirs {
			if subdir != "src" {
				ScanDirectory(path.Join(base, subdir), path.Join(dir, subdir))
			}
		}
	}
	
	return
}

func IsListed(name string) bool {
	if ListedTargets == 0 {
		return true
	}
	if Exclusive {
		return ListedDirs[name]
	}
	for lt := range ListedDirs {
		if strings.HasPrefix(name, lt) {
			return true
		}
	}
	return false
}

func RunGB() (err os.Error) {
	Build = Build || (!GenMake && !Clean) || (Makefiles && !Clean) || Install || Test

	ListedDirs = make(map[string]bool)

	args := os.Args[1:len(os.Args)]

	Recurse = true
	err = ScanDirectory(".", ".")
	if err != nil {
		return
	}
	
	for _, arg := range args {
		if arg[0] != '-' {
			ListedDirs[path.Clean(arg)] = true
		}
	}

	ListedPkgs := []*Package{}
	for _, pkg := range Packages {
		if IsListed(pkg.Dir) {
			ListedPkgs = append(ListedPkgs, pkg)
		}
	}

	if Scan {
		return
	}

	for _, pkg := range Packages {
		pkg.ResolveDeps()
	}

	if GoFMT {
		println("Running gofmt")
		for _, pkg := range ListedPkgs {
			err = pkg.GoFMT()
			if err != nil {
				return
			}
		}
	}

	if GenMake {

		fmt.Printf("(in .) generating build script\n")
		_, ferr := os.Stat("build")

		genBuild := true

		if ferr == nil {
			if !Force {
				fmt.Printf("'build' exists; overwrite? (y/n) ")
				var answer string
				fmt.Scanf("%s", &answer)
				genBuild = answer == "y" || answer == "Y"
			}
			os.Remove("build")
		}

		if genBuild {
			var buildFile *os.File
			buildFile, err = os.Open("build", os.O_CREATE|os.O_RDWR, 0755)

			for _, pkg := range ListedPkgs {
				pkg.AddToBuild(buildFile)
			}
			buildFile.Close()
		}
		for _, pkg := range ListedPkgs {
			err = pkg.GenerateMakefile()
			if err != nil {
				return
			}
		}

		if !Build {
			return
		}
	}

	if Clean && ListedTargets == 0 {
		println("Removing _obj")
		os.RemoveAll("_obj")
	}

	if Clean {
		for _, pkg := range ListedPkgs {
			pkg.Clean()
		}
	}

	if Build {
		if Concurrent {
			for _, pkg := range ListedPkgs {
				go pkg.Build()
			}
		}
		for _, pkg := range ListedPkgs {
			err = pkg.Build()
			if err != nil {
				return
			}
		}
	}

	if Test {
		for _, pkg := range ListedPkgs {
			if pkg.Name != "main" && len(pkg.TestSources) != 0 {
				err = pkg.Test()
				if err != nil {
					return
				}
			}
		}
	}

	if Install {
		for _, pkg := range ListedPkgs {
			err = pkg.Install()
			if err != nil {
				return
			}
		}
	}

	if !Clean {
		if PackagesBuilt > 1 {
			fmt.Printf("Built %d targets\n", PackagesBuilt)
		} else if PackagesBuilt == 1 {
			println("Built 1 targets")
		}
		if PackagesInstalled > 1 {
			fmt.Printf("Installed %d targets\n", PackagesInstalled)
		} else if PackagesInstalled == 1 {
			println("Installed 1 target")
		}
		if PackagesBuilt == 0 && PackagesInstalled == 0 && BrokenPackages == 0 {
			println("Up to date")
		}
		if BrokenPackages > 1 {
			fmt.Printf("%d broken targets\n", BrokenPackages)
		} else if BrokenPackages == 1 {
			println("1 broken target")
		}
	} else {
		if PackagesBuilt == 0 {
			println("No mess to clean")
		}
	}

	return
}

func Usage() {
	println("Usage: gb [-options] [directory list]")
	println("Options:")
	println(" ? print this usage text")
	println(" i install")
	println(" c clean")
	println(" b build after cleaning")
	println(" g use goinstall when appropriate")
	println(" p build packages in parallel, when possible")
	println(" s scan and list targets without building")
	println(" S scan and list targets and their dependencies without building")
	println(" t run tests")
	println(" e exclusive target list (do not build/clean/test/install a target unless its")
	println("   directory is listed")
	println(" v verbose")
	println(" m use makefiles, when possible")
	println(" M generate standard makefiles without building")
	println(" f force overwrite of existing makefiles")
	println(" F run gofmt on source files in targeted directories")
	println()
	//println("--------------------------------------------------------------------------------")
	println(" gb will identify any possible targets existing in subdirectories of the current")
	println("working directory, and act on them as appropriate. If a directory list is sup-")
	println("plied on the command line, only targets residing in those directories and their")
	println("dependencies will be built.")
	println()
	println(" If a target's directory contains a directory 'src', that directory will be")
	println("recursively searched for any source files, and no additional targets will be")
	println("created for directories found within the src directory.")
	println()
	println(" By default, the target built from a particular directory is the same as the")
	println("relative path to that directory. By providing a file 'target.gb' in the direct-")
	println("ory, one can change the target. This affects all subdirectories, as well. If a")
	println("directory 'a' has a target.gb file containing the line 'a.googlecode.com/hg/a',")
	println("then the directory 'a/b' will have the target 'a.googlecode.com/hg/a/b'.")
	println()
	println(" In addition to providing target.gb, the programmer can also put a comment in")
	println("one of the packages source files, before the package statement. The comment is")
	println("of the form '//target:<name>'. It has the same effect as providing target.gb. If")
	println("a target.gb file exists, it will be used over the comment.") 
	println()
	println(" The makefiles generated with the -M option will still allow each package to be")
	println("linked against each other package (if they are built in the correct order), by")
	println("copying binaries to a top level _obj directory and adding that directory to the")
	println("compile and link command line.")
}

func main() {
	GOOS, GOARCH, GOROOT = os.Getenv("GOOS"), os.Getenv("GOARCH"), os.Getenv("GOROOT")
	if GOOS == "" {
		println("Environental variable GOOS not set")
		return
	}
	if GOARCH == "" {
		println("Environental variable GOARCH not set")
		return
	}
	if GOROOT == "" {
		println("Environental variable GOROOT not set")
		return
	}
	for _, arg := range os.Args[1:len(os.Args)] {
		if len(arg) > 0 && arg[0] == '-' {
			for _, flag := range arg[1:len(arg)] {
				switch flag {
				case 'i':
					Install = true
				case 'c':
					Clean = true
				case 'b':
					Build = true
				case 's':
					Scan = true
				case 'S':
					Scan = true
					ScanList = true
				case 't':
					Test = true
				case 'e':
					Exclusive = true
				case 'v':
					Verbose = true
				case 'm':
					Makefiles = true
				case 'M':
					GenMake = true
				case 'f':
					Force = true
				case 'g':
					GoInstall = true
				case 'p':
					Concurrent = true
				case 'F':
					GoFMT = true
				default:
					Usage()
					return

				}
			}
		} else {
			ListedTargets++
		}
	}

	var err os.Error

	if Makefiles {
		MakeCMD, err = exec.LookPath("make")
		if err != nil {
			fmt.Printf("%v\n", err)
			return
		}
	}

	CompileCMD, err = exec.LookPath(GetCompilerName())
	if err != nil {
		fmt.Printf("Could not find %s in path\n", GetCompilerName())
		fmt.Printf("%v\n", err)
		return
	}

	LinkCMD, err = exec.LookPath(GetLinkerName())
	if err != nil {
		fmt.Printf("Could not find %s in path\n", GetLinkerName())
		fmt.Printf("%v\n", err)
		return
	}
	PackCMD, err = exec.LookPath("gopack")
	if err != nil {
		fmt.Printf("Could not find gopack in path\n")
		fmt.Printf("%v\n", err)
		return
	}
	CopyCMD, err = exec.LookPath("cp")
	if err != nil {
		//fmt.Printf("Could not find cp in path\n")
		//fmt.Printf("%v\n", err)
		//return
		CopyCMD = ""
	}
	GoInstallCMD, err = exec.LookPath("goinstall")
	if err != nil {
		fmt.Printf("Could not find goinstall in path\n")
		fmt.Printf("%v\n", err)
	}
	GoFMTCMD, err = exec.LookPath("gofmt")
	if err != nil {
		fmt.Printf("Could not find gofmt in path\n")
		fmt.Printf("%v\n", err)
	}

	GCArgs = []string{}
	GLArgs = []string{}

	if !Install {
		IncludeDir = "_obj"
		GCArgs = append(GCArgs, []string{"-I", IncludeDir}...)
		GLArgs = append(GLArgs, []string{"-L", IncludeDir}...)
	}

	err = RunGB()
	if err != nil {
		fmt.Printf("%v\n", err)
	}
}
