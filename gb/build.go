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

package main

import (
	"fmt"
	"os"
	"path"
)


func GetCompilerName() (name string) {
	switch GOARCH {
	case "amd64":
		return "6g"
	case "386":
		return "8g"
	case "arm":
		return "5g"
	}
	return
}

func GetLinkerName() (name string) {
	switch GOARCH {
	case "amd64":
		return "6l"
	case "386":
		return "8l"
	case "arm":
		return "5l"
	}
	return
}

func GetObjSuffix() (suffix string) {
	switch GOARCH {
	case "amd64":
		return ".6"
	case "386":
		return ".8"
	case "arm":
		return ".5"
	}
	return
}

func GetIBName() (name string) {
	return "_go_" + GetObjSuffix()
}

func ReverseDir(dir string) (rev string) {
	rev = "."
	for dir != "." && dir != "" {
		dir, _ = path.Split(path.Clean(dir))
		rev = path.Join(rev, "..")
	}
	return
}

func ReverseDirForwardSlash(dir string) (rev string) {
	rev = "."
	for dir != "." && dir != "" {
		dir, _ = path.Split(path.Clean(dir))
		rev += "/.."
	}
	return
}

func BuildPackage(pkg *Package) (err os.Error) {
	buildBlock <- true
	defer func() { <-buildBlock }()

	reverseDots := ""
	if !pkg.IsInGOROOT {
		reverseDots = ReverseDir(pkg.Dir)
	}
	pkgDest := path.Join(reverseDots, GetBuildDirPkg())
	//cmdDest := path.Join(reverseDots, GetBuildDirCmd())

	srcs := pkg.PkgSrc[pkg.Name]

	argv := []string{GetCompilerName()}
	if !pkg.IsInGOROOT {
		argv = append(argv, "-I", pkgDest)
	}
	argv = append(argv, "-o", GetIBName())
	argv = append(argv, srcs...)
	if Verbose {
		fmt.Printf("%v\n", argv)
	}
	err = RunExternal(CompileCMD, pkg.Dir, argv)
	if err != nil {
		return
	}

	//see if it was created
	if _, err = os.Stat(pkg.ib); err != nil {
		return os.NewError("compile error")
	}

	dst := path.Join(reverseDots, pkg.result)

	if pkg.IsCmd {
		os.MkdirAll(GetBuildDirCmd(), 0755)

		largs := []string{GetLinkerName()}
		if !pkg.IsInGOROOT {
			largs = append(largs, "-L", pkgDest)
		}
		largs = append(largs, "-o", dst, GetIBName())
		if Verbose {
			fmt.Printf("%v\n", largs)
		}
		err = RunExternal(LinkCMD, pkg.Dir, largs)
	} else {
		dstDir, _ := path.Split(pkg.result)
		if Verbose {
			fmt.Printf("Creating directory %s\n", dstDir)
		}
		os.MkdirAll(dstDir, 0755)

		argv = []string{"gopack", "grc", dst, GetIBName()}
		if Verbose {
			fmt.Printf("%v\n", argv)
		}
		if err = RunExternal(PackCMD, pkg.Dir, argv); err != nil {
			return
		}
	}

	var resInfo *os.FileInfo
	resInfo, err2 := os.Stat(pkg.result)
	if err2 == nil {
		pkg.BinTime = resInfo.Mtime_ns
	}

	return
}
func BuildTest(pkg *Package) (err os.Error) {

	reverseDots := ReverseDir(pkg.Dir)
	pkgDest := path.Join(reverseDots, GetBuildDirPkg())

	testIB := path.Join("_test", "_gotest_"+GetObjSuffix())

	argv := []string{GetCompilerName()}
	argv = append(argv, "-I", pkgDest)
	argv = append(argv, "-o", testIB)
	argv = append(argv, pkg.Sources...)
	argv = append(argv, pkg.TestSources...)

	if Verbose {
		fmt.Printf("%v\n", argv)
	}
	if err = RunExternal(CompileCMD, pkg.Dir, argv); err != nil {
		return
	}

	//see if it was created
	if _, err = os.Stat(path.Join(pkg.Dir, testIB)); err != nil {
		return os.NewError("compile error")
	}

	dst := path.Join("_test", "_obj", pkg.Target) + ".a"

	mkdirdst := path.Join(pkg.Dir, "_test", "_obj", pkg.Target) + ".a"
	dstDir, _ := path.Split(mkdirdst)
	os.MkdirAll(dstDir, 0755)

	argv = []string{"gopack", "grc", dst, testIB}
	if Verbose {
		fmt.Printf("%v\n", argv)
	}
	if err = RunExternal(PackCMD, pkg.Dir, argv); err != nil {
		return
	}

	testmainib := path.Join("_test", "_testmain"+GetObjSuffix())

	argv = []string{GetCompilerName()}
	argv = append(argv, "-I", path.Join("_test", "_obj"))
	argv = append(argv, "-I", pkgDest)
	argv = append(argv, "-o", testmainib)
	argv = append(argv, path.Join("_test", "_testmain.go"))

	if Verbose {
		fmt.Printf("%v\n", argv)
	}
	if err = RunExternal(CompileCMD, pkg.Dir, argv); err != nil {
		return
	}

	testBinary := "_testmain"
	if GOOS == "windows" {
		testBinary += ".exe"
	}

	largs := []string{GetLinkerName()}
	largs = append(largs, "-L", path.Join("_test", "_obj"))
	largs = append(largs, "-L", pkgDest)
	largs = append(largs, "-o", testBinary, testmainib)
	if Verbose {
		fmt.Printf("%v\n", largs)
	}
	if err = RunExternal(LinkCMD, pkg.Dir, largs); err != nil {
		return
	}

	if err = RunExternal(testBinary, pkg.Dir, []string{testBinary}); err != nil {
		return
	}

	return
}
func InstallPackage(pkg *Package) (err os.Error) {
	dstDir, _ := path.Split(pkg.installPath)
	_, dstName := path.Split(pkg.result)
	dstFile := path.Join(dstDir, dstName)
	err = os.MkdirAll(dstDir, 0755)
	if err != nil {
		return
	}

	which := "cmd"
	if pkg.Name != "main" {
		which = "pkg"
	}
	fmt.Printf("Installing %s \"%s\"\n", which, pkg.Target)

	Copy(".", pkg.result, dstFile)

	return
}

func CopyTheHardWay(cwd, src, dst string) (err os.Error) {
	srcpath := path.Join(cwd, src)

	if Verbose {
		fmt.Printf("Copying %s to %s\n", src, dst)
	}

	dstpath := dst
	if !path.IsAbs(dstpath) {
		dstpath = path.Join(cwd, dst)
	}

	var srcFile *os.File
	srcFile, err = os.Open(srcpath, os.O_RDONLY, 0)
	if err != nil {
		return
	}

	var dstFile *os.File
	dstFile, err = os.Open(dstpath, os.O_CREATE|os.O_RDWR, 0755)
	if err != nil {
		return
	}

	buffer := make([]byte, 1024)
	var cpErr os.Error
	for {
		var n int
		n, cpErr = srcFile.Read(buffer)
		if cpErr != nil {
			break
		}
		_, cpErr = dstFile.Write(buffer[0:n])
		if cpErr != nil {
			break
		}
	}
	if cpErr != os.EOF {
		err = cpErr
	}

	dstFile.Close()

	return
}

func Copy(cwd, src, dst string) (err os.Error) {
	if CopyCMD == "" {
		return CopyTheHardWay(cwd, src, dst)
	}

	argv := append([]string{"cp", "-f", src, dst})
	if Verbose {
		fmt.Printf("%v\n", argv)
	}
	if err = RunExternal(CopyCMD, cwd, argv); err != nil {
		return
	}

	return
}
