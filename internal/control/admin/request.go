package admin

import (
 "bytes"
 "encoding/json"
 "errors"
 "io"
 "net/http"
)

func decodeRequest(w http.ResponseWriter, r *http.Request, dest interface{}) bool {
 data,err:=io.ReadAll(http.MaxBytesReader(w,r.Body,4096))
 if err!=nil {
  var sizeErr *http.MaxBytesError
  if errors.As(err,&sizeErr) {http.Error(w,"request_too_large",413)} else {http.Error(w,"invalid_request",400)}
  return false
 }
 data=bytes.TrimSpace(data)
 if len(data)==0 || data[0]!='{' {http.Error(w,"invalid_request",400);return false}
 decoder:=json.NewDecoder(bytes.NewReader(data));decoder.DisallowUnknownFields()
 if err:=decoder.Decode(dest);err!=nil {http.Error(w,"invalid_request",400);return false}
 if err:=decoder.Decode(new(interface{}));err!=io.EOF {http.Error(w,"invalid_request",400);return false}
 return true
}

func writeServiceError(w http.ResponseWriter, err error) bool {
 switch {
 case errors.Is(err,ErrInvalidRequest):http.Error(w,"invalid_request",400)
 case errors.Is(err,ErrKeyNotFound),errors.Is(err,ErrUserNotFound),errors.Is(err,ErrCategoryNotFound):http.Error(w,"not_found",404)
 case errors.Is(err,ErrCategoryExists):http.Error(w,"category_exists",409)
 default:return false
 }
 return true
}
