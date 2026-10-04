package proxy

import (
 "context"
 "crypto/subtle"
 "io"
 "net/http"
 "strconv"
 "strings"
 "time"

 containerapi "github.com/docker/docker/api/types/container"
 "github.com/docker/docker/client"
 "github.com/docker/docker/pkg/stdcopy"
 "github.com/gorilla/websocket"
 "github.com/labstack/echo/v4"
 domain "github.com/chaitin/agent-compose/pkg/model"
)

type monkeyCodePreviewStore interface {
 GetSandbox(context.Context,string)(*domain.Sandbox,error)
 GetVMState(string)(domain.VMState,error)
}

// This extension is a node-authenticated raw TCP tunnel, not a public proxy.
// Docker exec reaches even development servers bound only to Guest loopback.
func RegisterMonkeyCodePreview(app *echo.Echo, store monkeyCodePreviewStore, token string) {
 slots:=make(chan struct{},64)
 app.GET("/internal/monkeycode/tcp/:sandbox/:port",func(c echo.Context)error{
  actual:=strings.TrimPrefix(c.Request().Header.Get("Authorization"),"Bearer ")
  if token=="" || subtle.ConstantTimeCompare([]byte(actual),[]byte(token))!=1 || c.Request().Header.Get("Origin")!="" { return c.NoContent(401) }
  port,err:=strconv.Atoi(c.Param("port"));if err!=nil || port<1 || port>65535{return c.NoContent(400)}
  sandbox,err:=store.GetSandbox(c.Request().Context(),c.Param("sandbox"));if err!=nil || sandbox==nil{return c.NoContent(404)}
  state,err:=store.GetVMState(sandbox.Summary.ID);if err!=nil || state.Driver!="docker" || state.BoxID==""{return c.NoContent(409)}
  select{case slots<-struct{}{}:defer func(){<-slots}();default:return c.NoContent(503)}
  ctx,cancel:=context.WithCancel(c.Request().Context());defer cancel()
  docker,err:=client.NewClientWithOpts(client.FromEnv,client.WithAPIVersionNegotiation());if err!=nil{return c.NoContent(502)};defer docker.Close()
  inspect,err:=docker.ContainerInspect(ctx,state.BoxID)
  if err!=nil || inspect.State==nil || !inspect.State.Running || inspect.Config==nil || inspect.Config.Labels["agent-compose.sandbox_id"]!=sandbox.Summary.ID || inspect.Config.Labels["agent-compose.driver"]!="docker"{return c.NoContent(409)}
  // EOF on Docker stdin shuts down the local socket and ends the helper.
  source:=`import os,socket,sys,threading
port=int(sys.argv[1])
try: connection=socket.create_connection(("127.0.0.1",port),5)
except OSError: connection=socket.create_connection(("::1",port),5)
connection.settimeout(None)
os.write(1,b"\x01")
def input_loop():
    try:
        while True:
            data=os.read(0,32768)
            if not data: break
            connection.sendall(data)
    except OSError: pass
    finally:
        try: connection.shutdown(socket.SHUT_RDWR)
        except OSError: pass
threading.Thread(target=input_loop,daemon=True).start()
try:
    while True:
        data=connection.recv(32768)
        if not data: break
        sys.stdout.buffer.write(data)
        sys.stdout.buffer.flush()
finally: connection.close()
`
  created,err:=docker.ContainerExecCreate(ctx,state.BoxID,containerapi.ExecOptions{AttachStdin:true,AttachStdout:true,AttachStderr:true,Cmd:[]string{"python3","-u","-c",source,strconv.Itoa(port)}})
  if err!=nil{return c.NoContent(502)}
  attached,err:=docker.ContainerExecAttach(ctx,created.ID,containerapi.ExecAttachOptions{})
  if err!=nil{return c.NoContent(502)}
  defer attached.Close()
  // Closing the write side delivers EOF, including when a WebSocket is revoked.
  defer func(){if cw,ok:=attached.Conn.(interface{CloseWrite()error});ok{_ = cw.CloseWrite()}}()
  outputR,outputW:=io.Pipe();defer outputR.Close()
  go func(){_,err:=stdcopy.StdCopy(outputW,io.Discard,attached.Reader);outputW.CloseWithError(err)}()
  ready:=make(chan error,1)
  go func(){b:=make([]byte,1);_,err:=io.ReadFull(outputR,b);if err==nil && b[0]!=1{err=io.ErrUnexpectedEOF};ready<-err}()
  select{case err=<-ready:if err!=nil{return c.NoContent(502)};case <-time.After(6*time.Second):return c.NoContent(502);case <-ctx.Done():return nil}
  upgrader:=websocket.Upgrader{ReadBufferSize:32768,WriteBufferSize:32768,CheckOrigin:func(r *http.Request)bool{return r.Header.Get("Origin")==""}}
  ws,err:=upgrader.Upgrade(c.Response(),c.Request(),nil);if err!=nil{return err};defer ws.Close()
  ws.SetReadLimit(1<<20)
  done:=make(chan struct{})
  go func(){
   defer close(done)
   b:=make([]byte,32768)
   for{n,err:=outputR.Read(b);if n>0{_ = ws.SetWriteDeadline(time.Now().Add(30*time.Second));if ws.WriteMessage(websocket.BinaryMessage,b[:n])!=nil{break}};if err!=nil{break}}
   _ = ws.WriteControl(websocket.CloseMessage,websocket.FormatCloseMessage(websocket.CloseNormalClosure,""),time.Now().Add(time.Second))
   ws.Close()
  }()
  for{
   kind,reader,err:=ws.NextReader();if err!=nil{break};if kind!=websocket.BinaryMessage{break}
   _ = attached.Conn.SetWriteDeadline(time.Now().Add(30*time.Second))
   if _,err=io.Copy(attached.Conn,reader);err!=nil{break}
  }
  // Wake both blocked copies before waiting for them; no goroutine retains the
  // Docker socket after the public client disconnects.
  if cw,ok:=attached.Conn.(interface{CloseWrite()error});ok{_ = cw.CloseWrite()}
  attached.Close();outputR.Close();ws.Close()
  select{case <-done:case <-time.After(time.Second):}
  return nil
 })
}
