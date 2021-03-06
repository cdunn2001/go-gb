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
	"sort"
	"fmt"
	"os"
	"bufio"
	"strings"
	"path"
	"path/filepath"
)

type Package struct {
	Dir, Base string

	Name, Target string

	IsCmd  bool
	Active bool

	ResultPath, InstallPath string

	IsCGo bool

	//these prevent multipath issues for tree following
	built, cleaned, addedToBuild, gofmted, scanned bool

	NeedsBuild, NeedsInstall, NeedsGoInstall bool

	GoSources  []string
	CGoSources []string
	CSrcs      []string
	AsmSrcs    []string
	Sources    []string // the list of all .go, .c, .s source in the target

	DeadSources []string // all .go, .c, .s files that will not be included in the build

	Objects []string

	PkgSrc    map[string][]string
	TestSrc   map[string][]string
	PkgCGoSrc map[string][]string

	SrcDeps map[string][]string
	Deps    []string
	DepPkgs []*Package

	TestSources []string
	TestDeps    []string
	TestFuncs   map[string][]string
	TestDepPkgs []*Package

	CGoCFlags  map[string][]string
	CGoLDFlags map[string][]string

	HasMakefile     bool
	MustUseMakefile bool
	IsInGOROOT      bool
	IsInGOPATH      string

	SourceTime, BinTime, InstTime, GOROOTPkgTime int64

	FailedToBuild bool

	//to make sure that only one thread works on a given package at a time
	block chan bool
}

func NewPackage(base, dir string) (this *Package, err os.Error) {
	finfo, err := os.Stat(dir)
	if err != nil || !finfo.IsDirectory() {
		err = os.NewError("not a directory")
		return
	}

	this = new(Package)
	this.block = make(chan bool, 1)
	this.Dir = path.Clean(dir)
	this.PkgSrc = make(map[string][]string)
	this.PkgCGoSrc = make(map[string][]string)
	this.TestSrc = make(map[string][]string)
	this.TestFuncs = make(map[string][]string)

	this.CGoCFlags = make(map[string][]string)
	this.CGoLDFlags = make(map[string][]string)

	if rel := GetRelative(filepath.Join(GOROOT, "src"), dir, CWD); !strings.HasPrefix(rel, "..") {
		this.IsInGOROOT = true
	}

	for _, gp := range GOPATHS {
		if rel := GetRelative(filepath.Join(gp, "src"), dir, CWD); !strings.HasPrefix(rel, "..") {
			this.IsInGOPATH = gp //say which gopath we're in
		}
	}

	err = this.ScanForSource()
	if err != nil {
		return
	}
	err = this.GetSourceDeps()
	if err != nil {
		//return
	}

	this.FilterDeadSource()

	this.Base = base
	this.DepPkgs = make([]*Package, 0)

	if strings.HasPrefix(this.Dir, "./") {
		this.Dir = this.Dir[2:len(this.Dir)]
	}

	if _, err2 := os.Stat(path.Join(this.Dir, "/Makefile")); err2 == nil {
		this.HasMakefile = true
	}
	if _, err2 := os.Stat(path.Join(this.Dir, "/makefile")); err2 == nil {
		this.HasMakefile = true
	}

	if !this.HasMakefile && this.IsInGOROOT {
		err = os.NewError("GOROOT pkg without makefile - not meant to be built")
		return
	}

	if len(this.Sources) == 0 {
		err = os.NewError("no source")
		return
	}

	for _, src := range this.Sources {
		var t int64
		t, err = StatTime(path.Join(this.Dir, src))
		if err != nil {
			err = os.NewError(fmt.Sprintf("'%s' just disappeared.\n", path.Join(this.Dir, src)))
			return
		}
		if t > this.SourceTime {
			this.SourceTime = t
		}
	}

	if err != nil {
		return
	}
	this.IsCmd = this.Name == "main"
	this.Objects = append(this.Objects, path.Join(this.Dir, GetIBName()))
	err = this.GetTarget()

	if !FilterPkg(this.Target) {
		err = os.NewError("Filtered package based on GOOS/GOARCH")
		return
	}

	if this.IsCmd && this.IsCGo {
		err = os.NewError(fmt.Sprintf("(in %s) cannot have a cgo cmd", this.Dir))
		ErrLog.Println(err)
		return
	}

	this.Active = (DoCmds && this.IsCmd) || (DoPkgs && !this.IsCmd)

	return
}

func (this *Package) DetectCycles() (cycle []*Package) {
	cycle = this.detectCycle(nil)
	return
}

func (this *Package) detectCycle(visited []*Package) (cycle []*Package) {
	for i, p := range visited {
		if p == this {
			cycle = visited[:i+1]
			return
		}
	}

	visited = append([]*Package{this}, visited...)

	for _, pkg := range this.DepPkgs {
		cycle = pkg.detectCycle(visited)
		if cycle != nil {
			return
		}
	}

	if Test {
		for _, pkg := range this.TestDepPkgs {
			if pkg == this {
				continue
			}
			cycle = pkg.detectCycle(visited)
			if cycle != nil {
				return
			}
		}
	}

	return
}

func (this *Package) ScanForSource() (err os.Error) {
	errch := make(chan os.Error)
	go func() {
		filepath.Walk(this.Dir, this, errch)
		close(errch)
	}()
	for fperr := range errch {
		ErrLog.Printf("Error while scanning: %s", fperr)
	}

	if len(this.AsmSrcs)+len(this.GoSources)+len(this.TestSources) == 0 { //allsources
		err = os.NewError("No source files in " + this.Dir)
	}

	this.IsCGo = this.IsCGo || len(this.CSrcs) /*+len(this.AsmSrcs)*/ > 0

	return
}

func (this *Package) FilterDeadSource() {

	deadset := make(map[string]bool)
	for _, ds := range this.DeadSources {
		deadset[ds] = true
	}
	for _, s := range this.PkgSrc[this.Name] {
		deadset[s] = false
	}
	for _, s := range this.CGoSources {
		deadset[s] = false
	}
	for _, s := range this.AsmSrcs {
		deadset[s] = false
	}

	this.DeadSources = []string{}
	for s, ok := range deadset {
		if ok {
			this.DeadSources = append(this.DeadSources, s)
		}
	}

}

func (this *Package) VisitDir(dpath string, f *os.FileInfo) bool {
	return dpath == this.Dir // || strings.HasPrefix(dpath, path.Join(this.Dir, "src"))
}
func (this *Package) VisitFile(fpath string, f *os.FileInfo) {
	//ignore hidden and temporary files
	if strings.HasPrefix(fpath, ".") {
		return
	}
	if strings.HasPrefix(fpath, "#") {
		return
	}
	//skip files generates by the cgo process
	if strings.HasSuffix(fpath, ".cgo1.go") {
		return
	}
	if strings.HasSuffix(fpath, ".cgo2.c") {
		return
	}
	pb := path.Base(fpath)
	if pb == "_cgo_gotypes.go" ||
		pb == "_cgo_import.c" ||
		pb == "__cgo_import.c" ||
		pb == "_cgo_main.c" ||
		pb == "_cgo_defun.c" {
		return
	}

	//skip files flagged for different OS/ARCH
	if !FilterFlag(fpath) {
		return
	}

	//no longer necessary since this goes into the _test dir
	if strings.HasSuffix(fpath, "_testmain.go") {
		return
	}

	rootl := len(this.Dir) + 1
	if this.Dir != "." {
		fpath = fpath[rootl:len(fpath)]
	}

	if strings.HasSuffix(fpath, ".go") ||
		strings.HasSuffix(fpath, ".c") ||
		strings.HasSuffix(fpath, ".s") {
		this.DeadSources = append(this.DeadSources, fpath)
	}

	if strings.HasSuffix(fpath, ".s") {
		this.AsmSrcs = append(this.AsmSrcs, fpath)
		this.Objects = append(this.Objects, fpath[:len(fpath)-2]+GetObjSuffix())
		this.Sources = append(this.Sources, fpath)
	}
	if strings.HasSuffix(fpath, ".go") {
		if strings.HasSuffix(fpath, "_test.go") {
			this.TestSources = append(this.TestSources, fpath)
			//} else if strings.HasPrefix(fpath, "cgo_") {
			//	this.CGoSources = append(this.CGoSources, fpath)
		} else {
			this.GoSources = append(this.GoSources, fpath)
		}
		this.Sources = append(this.Sources, fpath)
	}
	if strings.HasSuffix(fpath, ".c") {
		this.CSrcs = append(this.CSrcs, fpath)
		this.Sources = append(this.Sources, fpath)
	}

}

func (this *Package) GetSourceDeps() (err os.Error) {
	this.SrcDeps = make(map[string][]string)

	var nonCGoSrc []string

	for _, src := range this.GoSources {
		var fpkg, ftarget string
		var fdeps []string
		var cflags, ldflags []string
		fpkg, ftarget, fdeps, _, cflags, ldflags, err = GetDeps(path.Join(this.Dir, src))

		if err != nil {
			BrokenMsg = append(BrokenMsg, fmt.Sprintf("(in %s) %s", this.Dir, err.String()))
			continue
		}

		this.SrcDeps[src] = fdeps

		if ftarget != "" {
			this.Target = ftarget
		}
		if fpkg != "documentation" {
			if fpkg != "main" || this.Name == "" {
				this.Name = fpkg
			}
		}
		isCGoSrc := false
		for _, dep := range fdeps {
			if dep == "\"C\"" {
				isCGoSrc = true
				this.IsCGo = true
				this.CGoCFlags[fpkg] = append(this.CGoCFlags[fpkg], cflags...)
				this.CGoLDFlags[fpkg] = append(this.CGoLDFlags[fpkg], ldflags...)
			}
		}
		if isCGoSrc {
			this.SrcDeps[src] = append(this.SrcDeps[src], "\"runtime/cgo\"")
			this.CGoSources = append(this.CGoSources, src)
			this.PkgCGoSrc[fpkg] = append(this.PkgCGoSrc[fpkg], src)
		} else {
			nonCGoSrc = append(nonCGoSrc, src)
			this.PkgSrc[fpkg] = append(this.PkgSrc[fpkg], src)
		}
	}

	for fpkg, flags := range this.CGoCFlags {
		this.CGoCFlags[fpkg] = RemoveDups(flags)
	}
	for fpkg, flags := range this.CGoLDFlags {
		this.CGoLDFlags[fpkg] = RemoveDups(flags)
	}

	this.GoSources = nonCGoSrc

	for _, buildSrc := range this.PkgSrc[this.Name] {
		this.Deps = append(this.Deps, this.SrcDeps[buildSrc]...)
	}

	for _, buildSrc := range this.PkgCGoSrc[this.Name] {
		this.Deps = append(this.Deps, this.SrcDeps[buildSrc]...)
	}

	this.Deps = RemoveDups(this.Deps)

	if Test {
		for _, src := range this.TestSources {
			var fpkg, ftarget string
			var fdeps, ffuncs []string
			fpkg, ftarget, fdeps, ffuncs, _, _, err = GetDeps(path.Join(this.Dir, src))
			for _, dep := range fdeps {
				if dep == "\"C\"" {
					ErrLog.Printf("Test src %s wants to use cgo... too much effort.\n", src)
					continue
				}
			}
			/*
						//if there are no Test* or Benchmark* functions, forget it
						for _, ffunc := range ffuncs {
							if strings.HasPrefix(ffunc, "Test") {
								goto havetests
							}
							if strings.HasPrefix(ffunc, "Benchmark") {
								goto havetests
							}
						}
						fmt.Printf("skipping %s\n", src)
						continue
			havetests:
						fmt.Printf("using %s\n", src)
			*/
			this.TestSrc[fpkg] = append(this.TestSrc[fpkg], src)
			if err != nil {
				BrokenMsg = append(BrokenMsg, fmt.Sprintf("(in %s) %s", this.Dir, err.String()))
				break
			}
			if ftarget != "" {
				this.Target = ftarget
			}
			//this.Name = fpkg
			this.TestDeps = append(this.TestDeps, fdeps...)
			//this.Funcs = append(this.Funcs, ffuncs...)
			this.TestFuncs[fpkg] = append(this.TestFuncs[fpkg], ffuncs...)
		}
		this.TestDeps = RemoveDups(this.TestDeps)
	}
	return
}

func (this *Package) GetTarget() (err os.Error) {
	if !this.IsCmd && this.IsInGOROOT {
		//always the relative path
		this.Target = GetRelative(path.Join(GOROOT, "src", "cmd"), this.Dir, CWD)
		if !strings.HasPrefix(this.Target, "..") {
			if this.IsCGo {
				err = os.NewError("gb can't compile the GOROOT c cmds")
				return
			}
			this.IsCmd = true
			this.MustUseMakefile = true
		}
	}
	if !this.IsCmd && this.IsInGOROOT {
		//always the relative path
		this.Target = GetRelative(path.Join(GOROOT, "src", "pkg"), this.Dir, CWD)
		if strings.HasPrefix(this.Target, "..") {
			//if _, ok := this.PkgSrc["documentation"]; !ok && len(this.PkgSrc)==1 {
			err = os.NewError(fmt.Sprintf("(in %s) GOROOT pkg is not in $GOROOT/src/pkg", this.Dir))
			ErrLog.Println(err)
			//}
			return
		}
		//fmt.Printf("found goroot relative path for %s = %s\n", this.Dir, this.Target)
	} else if !this.IsCmd && this.IsInGOPATH != "" {
		//this is a gopath target
		this.Target = GetRelative(path.Join(this.IsInGOPATH, "src"), this.Dir, CWD)
		if strings.HasPrefix(this.Target, "..") {
			err = os.NewError(fmt.Sprintf("(in %s) GOPATH pkg is not in $GOPATH/src/pkg for GOPATH=%s", this.Dir, this.IsInGOPATH))
			ErrLog.Println(err)
			return
		}
	} else {
		if this.Target == "" {
			this.Target = this.Base

			if this.IsCmd {
				this.Target = path.Base(this.Dir)
				if this.Target == "." {
					this.Target = "main"
				}
			} else {
				if this.Target == "." {
					this.Target = "localpkg"
				}
				if this.Base == this.Dir && HasPathPrefix(this.Dir, "pkg") && this.Dir != "pkg" {
					this.Target = GetRelative("pkg", this.Dir, CWD)
				}
			}
		} else {
			this.Base = this.Target
		}

		tpath := path.Join(this.Dir, "/target.gb")
		fin, err2 := os.Open(tpath)
		if err2 == nil {
			bfrd := bufio.NewReader(fin)
			this.Target, err = bfrd.ReadString('\n')
			this.Target = strings.TrimSpace(this.Target)
			this.Base = this.Target
			if this.Target == "-" || this.Target == "--" {
				err = os.NewError("directory opts-out")
				return
			}
		}
	}

	this.Base = path.Clean(this.Base)
	this.Target = path.Clean(this.Target)

	err = nil

	if this.IsCmd {
		if GOOS == "windows" {
			this.Target += ".exe"
		}
	}

	if this.IsInGOROOT {
		if this.IsCmd {
			this.InstallPath = filepath.Join(GOBIN, this.Target)
		} else {
			this.InstallPath = filepath.Join(GOROOT, "pkg", GOOS+"_"+GOARCH, this.Target+".a")
		}
		this.ResultPath = this.InstallPath
	} else if this.IsInGOPATH != "" {
		if this.IsCmd {
			this.InstallPath = filepath.Join(this.IsInGOPATH, "bin", this.Target)
		} else {
			this.InstallPath = filepath.Join(this.IsInGOPATH, "pkg", GOOS+"_"+GOARCH, this.Target+".a")
		}
		this.ResultPath = this.InstallPath
	} else {
		if this.IsCmd {
			this.InstallPath = path.Join(GetInstallDirCmd(), this.Target)
			this.ResultPath = path.Join(GetBuildDirCmd(), this.Target)
		} else {
			this.InstallPath = path.Join(GetInstallDirPkg(), this.Target+".a")
			this.ResultPath = path.Join(GetBuildDirPkg(), this.Target+".a")
		}
	}

	if this.IsInGOROOT && ForceMakePkgs[this.Target] {
		this.MustUseMakefile = true
	}

	this.Stat()

	return
}

func (this *Package) PrintScan() {
	if this.scanned {
		return
	}
	this.scanned = true

	for _, pkg := range this.DepPkgs {
		pkg.PrintScan()
	}

	//build, install := this.Touched()
	bis := ""
	if !this.NeedsBuild {
		bis = " (up to date)"
	}
	if !this.NeedsInstall {
		bis = " (installed)"
	}
	var label string

	if this.IsCmd {
		label = "cmd"
	} else {
		label = "pkg"
	}
	if this.IsCGo && !this.IsCmd {
		label = "cgo"
	}
	if this.IsInGOROOT {
		label = "goroot " + label
	} else if this.IsInGOPATH != "" {
		label = "gopath " + label
	}

	displayDir := this.Dir
	if this.IsInGOROOT {
		displayDir = strings.Replace(displayDir, GOROOT, "$GOROOT", 1)
	}
	var prefix string
	if !this.IsInGOROOT && this.IsInGOPATH == "" {
		prefix = fmt.Sprintf("in %s: ", displayDir)
	}
	fmt.Printf("%s%s \"%s\"%s\n", prefix, label, this.Target, bis)
	if ScanList {
		fmt.Printf(" %s Deps: %v\n", this.Name, this.Deps)
		if Test {
			fmt.Printf(" %s TestDeps: %v\n", this.Name, this.TestDeps)
		}
	}
	if ScanListFiles {
		this.ListSource()
	}
}

func (this *Package) Stat() {
	this.BinTime, _ = StatTime(this.ResultPath)
	this.InstTime, _ = StatTime(this.InstallPath)
	/*
		resInfo, err := os.Stat(this.ResultPath)
		if resInfo != nil && err == nil {
			this.BinTime = resInfo.Mtime_ns
		} else {
			this.BinTime = 0
		}
		resInfo, err = os.Stat(this.InstallPath)
		if resInfo != nil && err == nil {
			this.InstTime = resInfo.Mtime_ns
		} else {
			this.InstTime = 0
		}
	*/
}

func (this *Package) CheckStatus() {
	b, i := this.Touched()
	this.NeedsBuild = b || this.NeedsBuild
	this.NeedsInstall = i || this.NeedsInstall
}

func (this *Package) ResolveDeps() (err os.Error) {
	CheckDeps := func(deps []string, test bool) (err os.Error) {
		for _, dep := range deps {
			if dep == "\"C\"" {
				this.IsCGo = true
				continue
			}
			if pkg, ok := Packages[dep]; ok {
				if test {
					this.TestDepPkgs = append(this.TestDepPkgs, pkg)
				} else {
					this.DepPkgs = append(this.DepPkgs, pkg)
				}
			} else {
				exists, when := PkgExistsInGOROOT(dep)
				if exists {
					if this.GOROOTPkgTime < when {
						this.GOROOTPkgTime = when
					}
				}
				if !IsGoInstallable(dep) {
					if !exists {
						//fmt.Printf("in %s: can't resolve pkg %s (maybe you aren't in the root?)\n", this.Dir, dep)
						err = os.NewError("unresolved packages")
					}
				} else {
					if GoInstallUpdate {
						this.NeedsBuild = true
					}
					if !exists {
						if !GoInstall {
							//fmt.Printf("in %s: can't resolve pkg %s (try using -g)\n", this.Dir, dep)
							err = os.NewError("unresolved packages")
						} else {
							this.NeedsGoInstall = true
							this.NeedsBuild = true
						}
					}

				}
			}
		}
		return
	}
	err = CheckDeps(this.Deps, false)
	if err != nil {
		return
	}
	err = CheckDeps(this.TestDeps, true)
	return
}

func (this *Package) Touched() (build, install bool) {
	var inTime int64

	build = this.NeedsBuild
	install = this.NeedsInstall

	for _, pkg := range this.DepPkgs {
		db, di := pkg.Touched()
		if db {
			build = true
		}
		if di {
			install = true
		}
		if pkg.BinTime > inTime {
			inTime = pkg.BinTime
		}
	}
	if this.GOROOTPkgTime > inTime {
		inTime = this.GOROOTPkgTime
	}

	if this.SourceTime > inTime {
		inTime = this.SourceTime
	}
	if inTime > this.BinTime {
		build = true
	}
	if this.InstTime < this.BinTime || this.InstTime < inTime {
		install = true
	}

	if build {
		install = true
	}

	return
}

func (this *Package) Build() (err os.Error) {
	this.block <- true
	defer func() {
		<-this.block
	}()

	defer func() {
		if err != nil {
			this.FailedToBuild = true
		}
	}()

	if this.FailedToBuild {
		err = os.NewError("Cannot build deps")
		return
	}
	if !this.NeedsBuild {
		return
	}
	if this.built {
		return
	}
	this.built = true

	if !TestCGO && (!this.HasMakefile && this.IsCGo) {
		ErrLog.Printf("(in %s) this is a cgo project; please create a makefile\n", this.Dir)
		return
	}

	if Exclusive && !ListedDirs[this.Dir] {
		return
	}

	inTime := this.GOROOTPkgTime

	if Concurrent {
		for _, pkg := range this.DepPkgs {
			go pkg.Build()
		}
	}

	for _, pkg := range this.DepPkgs {

		err = pkg.Build()
		if err != nil {
			return
		}
		if pkg.BinTime > inTime {
			inTime = pkg.BinTime
		}
	}
	if GoInstall {
		for _, dep := range this.Deps {
			if _, ok := Packages[dep]; !ok {
				goinstTime := GoInstallPkg(dep)
				if goinstTime > inTime {
					inTime = goinstTime
				}
			}
		}
	}

	if !this.Active {
		return
	}

	if this.SourceTime > inTime {
		inTime = this.SourceTime
	}

	if inTime > this.BinTime {
		which := "cmd"
		if this.Name != "main" {
			which = "pkg"
		}
		labelDir := this.Dir
		if HasPathPrefix(labelDir, GOROOT) {
			labelDir = "$GOROOT" + labelDir[len(GOROOT):]
		}
		fmt.Printf("(in %s) building %s \"%s\"\n", labelDir, which, this.Target)

		if (Makefiles || this.MustUseMakefile) && this.HasMakefile {
			err = MakeBuild(this)
		} else if this.IsCGo {
			err = BuildCgoPackage(this)
		} else {
			err = BuildPackage(this)
		}

		if err == nil {
			PackagesBuilt++
		} else {
			BrokenPackages++
			BrokenMsg = append(BrokenMsg, fmt.Sprintf("(in %s) could not build \"%s\"", this.Dir, this.Target))
		}

	}
	if err != nil {
		//this.CleanFiles()
	}

	if this.IsInGOROOT && this.HasMakefile {
		err = this.Install()
	}

	this.NeedsBuild = false
	this.Stat()

	return
}
func (this *Package) Test() (err os.Error) {
	for _, pkg := range this.TestDepPkgs {
		err = pkg.Build()
		if err != nil {
			return
		}
	}
	if GoInstall {
		for _, dep := range this.TestDeps {
			if _, ok := Packages[dep]; !ok {
				GoInstallPkg(dep)
			}
		}
	}

	if (Makefiles && this.HasMakefile) || this.IsCGo {
		err = MakeTest(this)
		return
	}

	testdir := path.Join(this.Dir, "_test")
	if Verbose {
		fmt.Printf(" Removing %s\n", testdir)
	}
	err = os.RemoveAll(testdir)

	fmt.Printf("(in %s) testing \"%s\"\n", this.Dir, this.Target)

	var pkgtests, pkgbenchmarks map[string][]string
	pkgtests = make(map[string][]string)
	pkgbenchmarks = make(map[string][]string)

	for name, funcs := range this.TestFuncs {
		for _, f := range funcs {
			if strings.HasPrefix(f, "Test") {
				pkgtests[name] = append(pkgtests[name], f)
			}
			if strings.HasPrefix(f, "Benchmark") {
				pkgbenchmarks[name] = append(pkgbenchmarks[name], f)
			}
		}
	}

	testsrc := path.Join(this.Dir, "_test", "_testmain.go")
	dstDir, _ := path.Split(testsrc)
	os.MkdirAll(dstDir, 0755)
	file, err := os.Create(testsrc)

	if err != nil {
		return
	}

	testSuite := &TestSuite{}

	testpkgMap := make(map[string]*TestPkg)

	for name, tests := range pkgtests {
		if _, ok := testpkgMap[name]; !ok {
			targ := name

			if name == this.Name {
				targ = this.Target
			}
			testpkgMap[name] = &TestPkg{
				PkgAlias:  name,
				PkgName:   name,
				PkgTarget: targ,
			}
		}

		tpkg := testpkgMap[name]

		for _, test := range tests {
			tpkg.TestFuncs = append(tpkg.TestFuncs, test)
		}
	}

	for name, benchmarks := range pkgbenchmarks {
		if _, ok := testpkgMap[name]; !ok {
			testpkgMap[name] = &TestPkg{
				PkgAlias: name,
				PkgName:  name,
			}
		}

		tpkg := testpkgMap[name]

		for _, benchmark := range benchmarks {
			tpkg.TestBenchmarks = append(tpkg.TestBenchmarks, benchmark)
		}
	}

	for _, tpkg := range testpkgMap {
		if tpkg.PkgName == "main" {
			tpkg.PkgAlias = "__main__"
		}
		testSuite.TestPkgs = append(testSuite.TestPkgs, tpkg)
	}

	err = TestmainTemplateExp.Execute(file, testSuite)
	if err != nil {
		return
	}

	err = BuildTest(this)

	this.Stat()

	return
}
/*
package main

import "go-glue.googlecode.com/hg/rlglue"
import "testing"
import __regexp__ "regexp"

var tests = []testing.InternalTest{
	{"rlglue.TestTaskSpec", rlglue.TestTaskSpec},
}
var benchmarks = []testing.InternalBenchmark{}

func main() {
	testing.Main(__regexp__.MatchString, tests)
	testing.RunBenchmarks(__regexp__.MatchString, benchmarks)
}

*/

func (this *Package) CleanFiles() (err os.Error) {
	defer func() {
		this.Stat()
		this.NeedsBuild = true
		this.NeedsInstall = true
	}()

	if Makefiles && this.HasMakefile {
		MakeClean(this)
		PackagesBuilt++
		return
	}

	if Nuke {
		if _, err2 := os.Stat(this.InstallPath); err2 == nil {
			reallyDoIt := true
			if !Force {
				fmt.Printf("Really nuke installed binary '%s'? (y/n) ", this.InstallPath)
				var answer string
				fmt.Scanf("%s", &answer)
				reallyDoIt = answer == "y" || answer == "Y"
			}
			if reallyDoIt {
				if Verbose {
					fmt.Printf(" Removing %s\n", this.InstallPath)
				}
				err = os.Remove(this.InstallPath)
			}
		}
	}

	ib := false
	res := false
	cgo := false
	test := false
	for _, obj := range this.Objects {
		if _, err2 := os.Stat(obj); err2 == nil {
			ib = true
		}
	}
	if _, err2 := os.Stat(this.ResultPath); err2 == nil {
		res = true
	}
	if this.IsCmd {
		_, bres := path.Split(this.ResultPath)
		if bres != this.ResultPath {
			if _, err2 := os.Stat(path.Join(this.Dir, bres)); err2 == nil {
				res = true
			}
		}
	}
	if _, err2 := os.Stat(path.Join(this.Dir, "_cgo")); err2 == nil {
		cgo = true
	}
	testdir := path.Join(this.Dir, "_test")
	if _, err2 := os.Stat(testdir); err2 == nil {
		test = true
	}
	if !ib && !res && !test && !cgo {
		return
	}
	fmt.Printf("Cleaning %s\n", this.Dir)
	for _, obj := range this.Objects {
		if Verbose {
			fmt.Printf(" Removing %s\n", obj)
		}
		err = os.Remove(obj)
	}
	if Verbose {
		fmt.Printf(" Removing %s\n", this.ResultPath)
	}
	err = os.Remove(this.ResultPath)
	if this.IsCmd {
		_, bres := path.Split(this.ResultPath)
		bres = path.Join(this.Dir, bres)
		if bres != this.ResultPath {
			if Verbose {
				fmt.Printf(" Removing %s\n", bres)
			}
			err = os.Remove(bres)
		}
	}
	if Verbose {
		fmt.Printf(" Removing %s\n", testdir)
	}
	err = os.RemoveAll(testdir)

	if this.IsCGo {
		err = CleanCGoPackage(this)
	}

	return
}

func (this *Package) Clean() (err os.Error) {
	if this.cleaned {
		return
	}
	this.cleaned = true
	if Exclusive && !ListedDirs[this.Dir] {
		return
	}

	for _, pkg := range this.DepPkgs {
		pkg.Clean()
	}

	if !this.Active {
		return
	}

	err = this.CleanFiles()

	PackagesBuilt++

	return
}
func (this *Package) Install() (err os.Error) {
	if !this.NeedsInstall {
		return
	}
	if Exclusive && !ListedDirs[this.Dir] {
		return
	}

	for _, pkg := range this.DepPkgs {
		pkg.Install()
	}

	if !this.Active {
		return
	}

	if !(Makefiles && this.HasMakefile) && this.InstTime < this.BinTime && !this.IsInGOROOT {
		err = InstallPackage(this)

		this.Stat()

		PackagesInstalled++
	}
	return
}

func (this *Package) ListSource() (err os.Error) {
	listFiles := func(files []string) {
		sortedFiles := sort.StringSlice(files)
		sortedFiles.Sort()
		for _, file := range sortedFiles {
			fmt.Printf("\t%s\n", file)
		}
	}
	listFileIfExists := func(file string) {
		f := path.Join(this.Dir, file)
		if _, err2 := os.Stat(f); err2 == nil {
			fmt.Printf("\t%s\n", file)
		}
	}

	listFileIfExists("Makefile")
	listFileIfExists("README")

	gosrc := append([]string{}, this.CGoSources...)
	gosrc = append(gosrc, this.PkgSrc[this.Name]...)

	listFiles(gosrc)
	listFiles(this.AsmSrcs)
	listFiles(this.CSrcs)

	for _, file := range this.DeadSources {
		fmt.Printf("\t*%s\n", file)
	}

	return
}

func (this *Package) CollectDistributionFiles(ch chan string) (err os.Error) {
	if Exclusive && !ListedDirs[this.Dir] {
		return
	}
	var f string
	f = path.Join(this.Dir, "Makefile")
	if _, err2 := os.Stat(f); err2 == nil {
		ch <- f
	}
	f = path.Join(this.Dir, "target.gb")
	if _, err2 := os.Stat(f); err2 == nil {
		ch <- f
	}
	f = path.Join(this.Dir, "README")
	if _, err2 := os.Stat(f); err2 == nil {
		ch <- f
	}
	for _, src := range this.GoSources {
		ch <- path.Join(this.Dir, src)
	}
	for _, src := range this.CSrcs {
		ch <- path.Join(this.Dir, src)
	}
	for _, src := range this.CGoSources {
		ch <- path.Join(this.Dir, src)
	}
	for _, src := range this.TestSources {
		ch <- path.Join(this.Dir, src)
	}

	for _, pkg := range this.DepPkgs {
		err = pkg.CollectDistributionFiles(ch)
		if err != nil {
			return
		}
	}

	for _, pkg := range this.TestDepPkgs {
		err = pkg.CollectDistributionFiles(ch)
		if err != nil {
			return
		}
	}

	return
}

func (this *Package) GenerateMakefile() (err os.Error) {
	if !this.Active {
		return
	}

	mpath := path.Join(this.Dir, "Makefile")

	_, ferr := os.Stat(mpath)
	if ferr == nil {
		if !Force {
			fmt.Printf("'%s' exists; overwrite? (y/n) ", mpath)
			var answer string
			fmt.Scanf("%s", &answer)
			if answer != "y" && answer != "Y" {
				err = nil
				return
			}
		}
		os.Remove(mpath)
	}

	which := "pkg"
	if this.IsCmd {
		which = "cmd"
	}
	fmt.Printf("(in %s) generating makefile for %s \"%s\"\n", this.Dir, which, this.Target)

	var file *os.File
	file, err = os.Create(mpath)

	if err != nil {
		return
	}

	reverseDots := ReverseDir(this.Dir)

	data := MakeData{
		Target:      this.Target,
		GBROOT:      reverseDots,
		GoFiles:     this.PkgSrc[this.Name],
		CopyLocal:   reverseDots != ".",
		BuildDirPkg: GetBuildDirPkg(),
		BuildDirCmd: GetBuildDirCmd(),
		GOPATHS:     GOPATHS,
	}
	for _, dep := range this.DepPkgs {
		data.LocalDeps = append(data.LocalDeps, dep.Target)
	}
	for _, asm := range this.AsmSrcs {
		base := asm[0 : len(asm)-2] // definitely ends with '.s', so this is safe
		asmObj := base + GetObjSuffix()
		data.AsmObjs = append(data.AsmObjs, asmObj)
	}

	if !this.IsCmd {
		if this.IsCGo {
			data.CGoFiles = this.PkgCGoSrc[this.Name]
			if len(this.CSrcs) != 0 {
				for _, src := range this.CSrcs {
					obj := src[:len(src)-2] + ".o"
					data.CObjs = append(data.CObjs, obj)
				}
			}
		}
		err = MakePkgTemplateExp.Execute(file, data)
	} else {
		if GOOS == "windows" && strings.HasSuffix(data.Target, ".exe") {
			data.Target = data.Target[0 : len(data.Target)-len(".exe")]
		}
		err = MakeCmdTemplateExp.Execute(file, data)
	}
	
	if err != nil {
		return
	}

	err = file.Close()

	return
}

func (this *Package) CollectGoInstall(gm map[string]bool) {
	for _, dep := range this.Deps {
		if IsGoInstallable(dep) {
			gm[dep] = true
		}
	}
}

func (this *Package) AddToBuild(bfile *os.File) (err os.Error) {
	if this.addedToBuild {
		return
	}
	this.addedToBuild = true

	if Exclusive && !ListedDirs[this.Dir] {
		return
	}

	if !this.Active {
		return
	}

	for _, pkg := range this.DepPkgs {
		err = pkg.AddToBuild(bfile)
		if err != nil {
			return
		}
	}
	_, err = fmt.Fprintf(bfile, "&& echo \"(in %s)\" && cd %s && make $1 && cd - > /dev/null \\\n", this.Dir, this.Dir)
	return
}

func (this *Package) GoFMT() (err os.Error) {
	if this.gofmted || (Exclusive && !ListedDirs[this.Dir]) {
		return
	}

	if !this.Active {
		return
	}

	this.gofmted = true

	for _, pkg := range this.DepPkgs {
		if Concurrent {
			go pkg.GoFMT()
		} else {
			err = pkg.GoFMT()
			if err != nil {
				return
			}
		}
	}

	fmt.Printf("(in %s) running gofmt\n", this.Dir)
	for _, src := range this.GoSources {
		err = RunGoFMT(this.Dir, src)
		if err != nil {
			return
		}
	}
	for _, src := range this.TestSources {
		err = RunGoFMT(this.Dir, src)
		if err != nil {
			return
		}
	}
	for _, src := range this.CGoSources {
		err = RunGoFMT(this.Dir, src)
		if err != nil {
			return
		}
	}

	this.Stat()

	return
}
