package dyaic

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"
)

func DyaicCommit(loc string, bs bool) {
	if loc == "" {
		loc = TempLocation
	}
	locLen := len(loc)
	err := filepath.Walk(loc, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rLoc := path[locLen:]
		repoLoc := RepoLocation + rLoc
		repoInfo, err := os.Stat(repoLoc)

		if Exist(err) {
			if info.IsDir() {
				return nil
			}
			if info.ModTime().After(repoInfo.ModTime()) { // file has been modified, sync needed
				fmt.Println("File has been modified:", rLoc)
				patchName := repoLoc + ".patch"
				if bs {
					GenBSPatch(repoLoc, path, patchName)
					BSPatch(repoLoc, repoLoc, patchName, true)
				} else {
					GenPatch(repoLoc, path, patchName)
					Patch(repoLoc, repoLoc, patchName, true)
				}
				fmt.Println("Updated.")
				// TODO: send changes tx
				// TODO: sync changes with other nodes
			}
		} else { // new file (or folder), creation needed
			if info.IsDir() {
				fmt.Println("Creating folder:", repoLoc)
				err = os.Mkdir(repoLoc, 0755)
				if err != nil {
					return err
				}
			} else {
				fmt.Println("New file:", rLoc)
				Copy(path, repoLoc)
				fmt.Println("Copied.")
				// TODO: send file tx
				// TODO: sync file with other nodes
			}
		}
		return nil
	})
	if err != nil {
		log.Panic(err)
	}
}

func hashFile(loc string) {
	hashBegin := time.Now()
	if loc == "" {
		loc = TempLocation
	}
	fmt.Println(Md5File(loc))
	hashEnd := time.Now()
	fmt.Println(hashEnd.Sub(hashBegin))
}

func DyaicPatch(loc string, bs bool) {
	if loc == "" {
		loc = TempLocation
	}
	locLen := len(loc)
	err := filepath.Walk(loc, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rLoc := path[locLen:]
		repoLoc := RepoLocation + rLoc
		//repoInfo, err := os.Stat(repoLoc)

		if Exist(err) {
			if info.IsDir() {
				return nil
			}
			if !SameFile(path, repoLoc) { // file has been modified, sync needed
				fmt.Println("File has been modified:", rLoc, ", file size: ", info.Size())
				patchName := repoLoc + ".patch"
				if bs {
					GenBSPatch(repoLoc, path, patchName)
				} else {
					GenPatch(repoLoc, path, patchName)
				}
				fmt.Println("Generated patch file ", repoLoc, ".patch")
			}
		} else { // new file (or folder), creation needed
			if info.IsDir() {
				fmt.Println("New folder:", repoLoc)
			} else {
				fmt.Println("New file:", rLoc)
			}
		}
		return nil
	})
	if err != nil {
		log.Panic(err)
	}
}

func DyaicPrintDiff(loc string) {
	if loc == "" {
		loc = TempLocation
	}
	locLen := len(loc)
	err := filepath.Walk(loc, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rLoc := path[locLen:]
		repoLoc := RepoLocation + rLoc
		repoInfo, err := os.Stat(repoLoc)

		if Exist(err) {
			if info.IsDir() {
				return nil
			}
			if info.ModTime().After(repoInfo.ModTime()) { // file has been modified, sync needed
				chs := GenerateChanges(repoLoc, path)
				if len(chs.Item) == 0 {
					return nil
				}
				fmt.Println("File has been modified:", rLoc)
				ShowDiff(repoLoc, path)
			}
		} else { // new file (or folder), creation needed
			if info.IsDir() {
				fmt.Println("New folder:", repoLoc)
			} else {
				fmt.Println("New file:", rLoc)
			}
		}
		return nil
	})
	if err != nil {
		log.Panic(err)
	}
}

func saveDiff(loc string) {
	if loc == "" {
		loc = TempLocation
	}
	locLen := len(loc)
	err := filepath.Walk(loc, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rLoc := path[locLen:]
		repoLoc := RepoLocation + rLoc
		repoInfo, err := os.Stat(repoLoc)

		if Exist(err) {
			if info.IsDir() {
				return nil
			}
			if info.ModTime().After(repoInfo.ModTime()) { // file has been modified, sync needed
				chs := GenerateChanges(repoLoc, path)
				if len(chs.Item) == 0 {
					return nil
				}
				fmt.Println("File has been modified:", rLoc)
				SaveDyaicDiff(repoLoc, path)
			}
		} else { // new file (or folder), creation needed
			if info.IsDir() {
				fmt.Println("New folder:", repoLoc)
			} else {
				fmt.Println("New file:", rLoc)
			}
		}
		return nil
	})
	if err != nil {
		log.Panic(err)
	}
}

func DyaicPrintFolder(loc string) {
	if loc == "" {
		loc = TempLocation
	}
	err := filepath.Walk(loc, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		fmt.Println(path, info.ModTime(), info.Size())
		return nil
	})
	if err != nil {
		log.Panic(err)
	}
}

func DyaicWatch(loc string) {
	watcher := Watch(loc)
	defer watcher.Close()
	select {}
}
