package dyaic

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"time"
)

func GenPatch(old, new, patchName string) {
	fmt.Println(patchName, ": diff begin")
	beginTime := time.Now()
	patch, err := exec.Command("diff", old, new).Output()
	fmt.Println(string(patch))
	// diff returns exit code 1 if diff is found, should not panic this "error"
	//if err != nil {
	//	log.Panic(err)
	//}
	err = ioutil.WriteFile(patchName+".patch", patch, 0644)
	if err != nil {
		log.Panic(err)
	}
	endTime := time.Now()
	fmt.Println(patchName, ": diff finished in", endTime.Sub(beginTime))
}

func Patch(old, new, patchName string, clean bool) {
	cmd := exec.Command("patch", old, "-i", patchName, "-o", new)
	err := cmd.Run()
	if err != nil {
		log.Panic(err)
	}
	if clean {
		err = os.Remove(patchName)
		if err != nil {
			log.Panic(err)
		}
	}
}

func GenBSPatch(old, new, patchName string) {
	cmd := exec.Command("bsdiff", old, new, patchName)
	beginTime := time.Now()
	fmt.Println(patchName, ": bsdiff begin")
	err := cmd.Run()
	if err != nil { // bsdiff returns 0 on success and -1 on failure
		log.Panic(err)
	}
	endTime := time.Now()
	fmt.Println(patchName, ": bsdiff finished in", endTime.Sub(beginTime))
}

func BSPatch(old, new, patchName string, clean bool) {
	cmd := exec.Command("bspatch", old, new, patchName)
	err := cmd.Run()
	if err != nil {
		log.Panic(err)
	}
	if clean {
		err = os.Remove(patchName)
		if err != nil {
			log.Panic(err)
		}
	}
}