package main

// users creates the accounts a class works the review exercise with (see docs/ROADMAP.md, block
// D): the instructor, who distributes the work, analyses and manages accounts, and coded facial
// examiners E01, E02, ... One code, drawn at random, is the planting account (plant): it decides
// some pairs wrongly on purpose, so blind verification and inconsistence have something to catch.
// Codes keep names off the projector, and keep the planting account looking like any student.
//
// Who is who stays out of trackid: the key file (codes, passwords, which code plants) is written
// to out/, which git ignores. Each account has its own password. Run it again and it reuses the
// key file and leaves existing accounts as they are.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"

	"github.com/rodfileto/trackid/auth"
)

// instructorUsername is the instructor's account.
const instructorUsername = "instrutor"

// classKey is the key file: who each code is (names filled in by the instructor) and which code
// plants errors. It never goes into trackid.
type classKey struct {
	Instructor account   `json:"instructor"`
	Examiners  []account `json:"examiners"`
	Planter    string    `json:"planter"` // the code of the planting account
}

type account struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Name     string `json:"name,omitempty"` // the student's name, for the instructor's eyes only
}

func cmdUsers(args []string) {
	fs := flag.NewFlagSet("users", flag.ExitOnError)
	envFile := fs.String("env", "", "dotenv file with DATABASE_URL (and EMAIL_DOMAIN)")
	examiners := fs.Int("examiners", 12, "how many students examine (one more coded account plants errors)")
	keyPath := fs.String("key", "out/class-key.json", "key file: codes, passwords and the planting account (kept out of trackid and git)")
	fs.Parse(args)

	key, err := readClassKey(*keyPath)
	fresh := errors.Is(err, os.ErrNotExist)
	switch {
	case fresh:
		key = newClassKey(*examiners)
		writeJSON(*keyPath, key)
		fmt.Printf("wrote %s\n", *keyPath)
	case err != nil:
		log.Fatal(err)
	default:
		fmt.Printf("reusing %s\n", *keyPath)
	}

	ctx := context.Background()
	sqlDB := connect(ctx, *envFile)
	defer sqlDB.Close()
	repo := auth.NewRepository(sqlDB)
	create := func(a account, access auth.Access) string {
		if _, err := repo.FindByStaffNumber(ctx, a.Username); err == nil {
			if fresh {
				// Made with another key file: its password is not this one's.
				return "EXISTS: left as it is, its password is NOT the one in the new key file (load a fresh database for a class)"
			}
			return "exists, left as it is"
		} else if !errors.Is(err, sql.ErrNoRows) {
			log.Fatalf("users: %v", err)
		}
		if _, err := repo.Create(ctx, auth.NewUser{FirstName: a.Username, LastName: "Sim", StaffNumber: a.Username, JobTitle: "Perito (sim)",
			Username: a.Username, Password: a.Password, Access: access}, os.Getenv("EMAIL_DOMAIN")); err != nil {
			log.Fatalf("users: %s: %v", a.Username, err)
		}
		return "created"
	}

	fmt.Printf("\n%-10s  %-34s  %s\n", "staff no.", "profile", "")
	fmt.Printf("%-10s  %-34s  %s\n", key.Instructor.Username, "admin, intelligence, coordinator",
		create(key.Instructor, auth.Access{AccountRole: auth.AccountRoleAdmin, IntelligenceAnalyst: true, Coordinator: true, IsActive: true}))
	for _, e := range key.Examiners {
		// The planting account is created inactive, so distribution never hands it students' work;
		// plant activates it while it works.
		status := create(e, auth.Access{AccountRole: auth.AccountRoleUser, ExaminesFacial: true, IsActive: e.Username != key.Planter})
		fmt.Printf("%-10s  %-34s  %s\n", e.Username, "facial examiner", status)
	}
	fmt.Printf("\npasswords, and which code plants errors, are in %s: hand each student their own code and password\n", *keyPath)
}

func newClassKey(n int) classKey {
	key := classKey{Instructor: account{Username: instructorUsername, Password: password()}}
	for i := 1; i <= n+1; i++ {
		key.Examiners = append(key.Examiners, account{Username: fmt.Sprintf("E%02d", i), Password: password()})
	}
	pick, err := rand.Int(rand.Reader, big.NewInt(int64(len(key.Examiners))))
	if err != nil {
		log.Fatal(err)
	}
	key.Planter = key.Examiners[pick.Int64()].Username
	return key
}

func readClassKey(path string) (classKey, error) {
	var key classKey
	data, err := os.ReadFile(path)
	if err != nil {
		return key, err
	}
	if err := json.Unmarshal(data, &key); err != nil {
		return key, fmt.Errorf("%s: %w", path, err)
	}
	return key, nil
}

// password is a random password a student can type: 10 characters from an alphabet without
// look-alikes.
func password() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			log.Fatal(err)
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}
