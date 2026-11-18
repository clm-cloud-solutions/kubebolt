package auth

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// The cost only changes how long a hash takes, never whether a password
// matches: the tests below still create, compare and rotate real hashes.
func init() { passwordHashCost = bcrypt.MinCost }

func TestPasswordHashCost_ProductionFloor(t *testing.T) {
	if productionPasswordHashCost < 12 {
		t.Fatalf("production bcrypt cost = %d; it must stay at 12 or above", productionPasswordHashCost)
	}
}
