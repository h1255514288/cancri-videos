package admin

import (
 "context"
 "errors"
 "strconv"
 "time"

 "github.com/golang-jwt/jwt/v5"
)

type adminClaims struct {
 AdminID int64 `json:"admin_id"`
 jwt.RegisteredClaims
}

type adminContextKey struct{}

func (s *Service) signAdminToken(id int64, expiry time.Time) (string, error) {
 if len(s.secret) < 32 { return "", errors.New("admin secret must contain at least 32 bytes") }
 c := adminClaims{AdminID:id, RegisteredClaims:jwt.RegisteredClaims{
 Subject:strconv.FormatInt(id,10), Issuer:"vds-control", Audience:jwt.ClaimStrings{"vds-admin"},
 IssuedAt:jwt.NewNumericDate(time.Now().UTC()), ExpiresAt:jwt.NewNumericDate(expiry),
 }}
 return jwt.NewWithClaims(jwt.SigningMethodHS256,c).SignedString([]byte(s.secret))
}

func (s *Service) authenticate(ctx context.Context, token string) (int64,error) {
 if len(s.secret)<32 { return 0,ErrInvalidCredentials }
 c:= &adminClaims{}
 parsed,err:=jwt.ParseWithClaims(token,c,func(t *jwt.Token)(interface{},error){return []byte(s.secret),nil},jwt.WithValidMethods([]string{"HS256"}),jwt.WithExpirationRequired(),jwt.WithIssuer("vds-control"),jwt.WithAudience("vds-admin"))
 if err!=nil || !parsed.Valid || c.AdminID<=0 || c.Subject!=strconv.FormatInt(c.AdminID,10) {return 0,ErrInvalidCredentials}
 var status string
 if err:=s.db.GetContext(ctx,&status,`SELECT status FROM admins WHERE id=$1`,c.AdminID);err!=nil {return 0,err}
 if status!="active" {return 0,ErrInvalidCredentials}
 return c.AdminID,nil
}
