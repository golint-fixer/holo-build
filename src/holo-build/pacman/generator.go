/*******************************************************************************
*
* Copyright 2015 Stefan Majewsky <majewsky@gmx.net>
*
* This file is part of Holo.
*
* Holo is free software: you can redistribute it and/or modify it under the
* terms of the GNU General Public License as published by the Free Software
* Foundation, either version 3 of the License, or (at your option) any later
* version.
*
* Holo is distributed in the hope that it will be useful, but WITHOUT ANY
* WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR
* A PARTICULAR PURPOSE. See the GNU General Public License for more details.
*
* You should have received a copy of the GNU General Public License along with
* Holo. If not, see <http://www.gnu.org/licenses/>.
*
*******************************************************************************/

package pacman

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"../common"
)

//Generator is the common.Generator for Pacman packages (as used by Arch Linux
//and derivatives).
type Generator struct{}

//RecommendedFileName implements the common.Generator interface.
func (g *Generator) RecommendedFileName(pkg *common.Package) string {
	//this is called after Build(), so we can assume that package name,
	//version, etc. were already validated
	return fmt.Sprintf("%s-%s-any.pkg.tar.xz", pkg.Name, fullVersionString(pkg))
}

//BuildInMemory implements the common.Generator interface.
func (g *Generator) BuildInMemory(pkg *common.Package, buildReproducibly bool) ([]byte, error) {
	return nil, common.UnsupportedBuildMethodError
}

//Build implements the common.Generator interface.
func (g *Generator) Build(pkg *common.Package, rootPath string, buildReproducibly bool) ([]byte, error) {
	//write .PKGINFO
	err := writePKGINFO(pkg, rootPath, buildReproducibly)
	if err != nil {
		return nil, fmt.Errorf("Failed to write .PKGINFO: %s", err.Error())
	}

	//write .INSTALL
	err = writeINSTALL(pkg, rootPath, buildReproducibly)
	if err != nil {
		return nil, fmt.Errorf("Failed to write .INSTALL: %s", err.Error())
	}

	//write mtree
	err = writeMTREE(rootPath, buildReproducibly)
	if err != nil {
		return nil, fmt.Errorf("Failed to write .MTREE: %s", err.Error())
	}

	//compress package
	return compressPackage(rootPath)
}

func fullVersionString(pkg *common.Package) string {
	//pkg.Version may not contain dashes in pacman packages, so replace "-" with "_"
	str := fmt.Sprintf("%s-%d", strings.Replace(pkg.Version, "-", "_", -1), pkg.Release)
	if pkg.Epoch > 0 {
		str = fmt.Sprintf("%d:%s", pkg.Epoch, str)
	}
	return str
}

func writePKGINFO(pkg *common.Package, rootPath string, buildReproducibly bool) error {
	//get fakeroot version
	fakerootVersionString, err := exec.Command("fakeroot", "--version").Output()
	if err != nil {
		return err
	}

	//normalize package description like makepkg does
	desc := regexp.MustCompile(`\s+`).ReplaceAllString(strings.TrimSpace(pkg.Description), " ")

	//generate .PKGINFO
	contents := ""
	if buildReproducibly {
		contents = "# Generated by holo-build in reproducible mode\n"
	} else {
		contents = fmt.Sprintf("# Generated by holo-build %s\n", common.VersionString())
		contents += fmt.Sprintf("# using %s\n", strings.TrimSpace(string(fakerootVersionString)))
	}
	contents += fmt.Sprintf("pkgname = %s\n", pkg.Name)
	contents += fmt.Sprintf("pkgver = %s\n", fullVersionString(pkg))
	contents += fmt.Sprintf("pkgdesc = %s\n", desc)
	contents += "url = \n"
	if !buildReproducibly {
		contents += fmt.Sprintf("builddate = %d\n", time.Now().Unix())
	}
	if pkg.Author == "" {
		contents += "packager = Unknown Packager\n"
	} else {
		contents += fmt.Sprintf("packager = %s\n", pkg.Author)
	}
	contents += fmt.Sprintf("size = %d\n", pkg.InstalledSizeInBytes())
	contents += "arch = any\n"
	contents += "license = custom:none\n"
	contents += compilePackageRelations("replaces", pkg.Replaces)
	contents += compilePackageRelations("conflict", pkg.Conflicts)
	contents += compilePackageRelations("provides", pkg.Provides)
	contents += compileBackupMarkers(pkg)
	requires, err := compilePackageRequirements(pkg.Requires)
	if err != nil {
		return err
	}
	contents += requires

	//we used holo-build to build this, so the build depends on this package
	contents += "makedepend = holo-build\n"
	//these makepkgopt are fabricated (well, duh) and describe the behavior of
	//holo-build in terms of these options
	contents += "makepkgopt = !strip\n"
	contents += "makepkgopt = docs\n"
	contents += "makepkgopt = libtool\n"
	contents += "makepkgopt = staticlibs\n"
	contents += "makepkgopt = emptydirs\n"
	contents += "makepkgopt = !zipman\n"
	contents += "makepkgopt = !purge\n"
	contents += "makepkgopt = !upx\n"
	contents += "makepkgopt = !debug\n"

	//write .PKGINFO
	return common.WriteFile(filepath.Join(rootPath, ".PKGINFO"), []byte(contents), 0644, buildReproducibly)
}

func compileBackupMarkers(pkg *common.Package) string {
	var lines []string
	for _, entry := range pkg.FSEntries {
		if entry.Type == common.FSEntryTypeRegular && !strings.HasPrefix(entry.Path, "/usr/share/holo/") {
			lines = append(lines, fmt.Sprintf("backup = %s\n", strings.TrimPrefix(entry.Path, "/")))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "")
}

func writeINSTALL(pkg *common.Package, rootPath string, buildReproducibly bool) error {
	//assemble the contents for the .INSTALL file
	contents := ""
	if script := strings.TrimSpace(pkg.SetupScript); script != "" {
		contents += fmt.Sprintf("post_install() {\n%s\n}\npost_upgrade() {\npost_install\n}\n", script)
	}
	if script := strings.TrimSpace(pkg.CleanupScript); script != "" {
		contents += fmt.Sprintf("post_remove() {\n%s\n}\n", script)
	}

	//do we need the .INSTALL file at all?
	if contents == "" {
		return nil
	}

	return common.WriteFile(filepath.Join(rootPath, ".INSTALL"), []byte(contents), 0644, buildReproducibly)
}

func writeMTREE(rootPath string, buildReproducibly bool) error {
	//list all desired entries in rootPath
	entries, err := filepath.Glob(filepath.Join(rootPath, "*"))
	if err != nil {
		return err
	}
	targets := make([]string, 0, len(entries))
	for _, entry := range entries {
		target, err := filepath.Rel(rootPath, entry)
		if err != nil {
			return err
		}
		//filepath.Rel() may result in "foo" instead of "./foo" sometimes, but
		//makepkg has .MTREEs with "./foo" paths, so enforce that
		if !strings.HasPrefix(target, "./") {
			target = "./" + target
		}
		targets = append(targets, target)
	}

	//generate mtree data
	cmd := exec.Command(
		//using standardized language settings...
		"env", append([]string{"LANG=C",
			//...generate an archive...
			"bsdtar", "-czf", ".MTREE",
			//...in mtree format with only the required filesystem metadata
			"--format=mtree", "--options=!all,use-set,type,uid,gid,mode,time,size,md5,sha256,link",
			//of these things
		}, targets...)...,
	)
	cmd.Dir = rootPath
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		return err
	}

	err = os.Chmod(filepath.Join(rootPath, ".MTREE"), 0644)
	if err != nil {
		return err
	}

	if buildReproducibly {
		return common.ResetTimestamp(filepath.Join(rootPath, ".MTREE"))
	}
	return nil
}

func compressPackage(rootPath string) ([]byte, error) {
	cmd := exec.Command(
		//using standardized language settings...
		"env", "LANG=C",
		//...generate a .tar.xz archive...
		"bsdtar", "-cJf", "-",
		//...with the leading "./" path element stripped...
		"--strip-components", "1",
		//...of the working directory (== rootPath)
		".",
	)
	cmd.Dir = rootPath
	cmd.Stderr = os.Stderr
	return cmd.Output()
}
