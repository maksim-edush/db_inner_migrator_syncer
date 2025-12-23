package rbac

type Role string

const (
	RoleUser    Role = "user"
	RoleManager Role = "manager"
	RoleAdmin   Role = "admin"
)

func Allows(user Role, allowed ...Role) bool {
	for _, role := range allowed {
		if user == role {
			return true
		}
	}
	return false
}
