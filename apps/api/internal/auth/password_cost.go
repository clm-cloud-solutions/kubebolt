package auth

// productionPasswordHashCost is the bcrypt cost every password is stored with.
// TestPasswordHashCost_ProductionFloor keeps it at 12 or above.
const productionPasswordHashCost = 12

// passwordHashCost is what the stores hash with: always the production cost,
// except in this package's own tests (password_cost_test.go), where one hash
// at 12 takes seconds under -race and the suite stores dozens of passwords.
var passwordHashCost = productionPasswordHashCost
