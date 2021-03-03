package main

import (
	"fmt"
	"time"

	"github.com/go-git/go-git/v5"
)

func main() {
	r, err := git.PlainOpen(".")
	if err != nil {
		panic(err)
	}
	commits, err := r.Log(&git.LogOptions{})
	if err != nil {
		panic(err)
	}
	commit, err := commits.Next()
	if err != nil {
		panic(err)
	}

	lastCommitDate := commit.Author.When
	sinceLastCommit := time.Now().Sub(lastCommitDate)
	if sinceLastCommit.Hours()/24 > 7 {
		fmt.Printf("最後のブログ更新から %d 日以上経っています。", int(sinceLastCommit.Hours()/24))
	}
}