package rpc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"go/token"
	"io"
	"reflect"

	"github.com/vmihailenco/msgpack/v5"
)

var errMsg = errors.New("read error message")

func writeFrame(w io.Writer, buf []byte) error {
	l := len(buf)

	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(l))
	_, err := w.Write(lenBuf[:])
	if err != nil {
		return err
	}
	_, err = w.Write(buf)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	buff := make([]byte, 4)
	_, err := io.ReadFull(r, buff)
	if err != nil {
		return nil, err
	}

	l := binary.LittleEndian.Uint32(buff)

	buff = make([]byte, l)
	_, err = io.ReadFull(r, buff)
	if err != nil {
		return nil, err
	}

	return buff, nil
}

func convertArg(field reflect.Value, argV reflect.Value) error {
	var fieldPtr reflect.Value
	var ptr bool
	for field.Kind() == reflect.Ptr {
		fieldPtr = field
		ptr = true
		field = field.Elem()
	}
	for argV.Kind() == reflect.Ptr {
		argV = argV.Elem()
	}
	if field.Type() == argV.Type() {
		field.Set(argV)
	} else if argV.Type().ConvertibleTo(field.Type()) {
		field.Set(argV.Convert(field.Type()))
	} else if ptr {
		b, err := msgpack.Marshal(argV.Interface())
		if err != nil {
			return err
		}

		err = msgpack.Unmarshal(b, fieldPtr.Interface())
		if err != nil {
			return err
		}
	}
	return nil
}

func readArg(v reflect.Value, args map[interface{}]interface{}) error {
	if v.Elem().Kind() == reflect.Struct {
		v = v.Elem()
		for i := 0; i < v.NumField(); i++ {
			arg, succ := args[int8(i+1)]
			if succ {
				err := convertArg(v.Field(i), reflect.ValueOf(arg))
				if err != nil {
					return err
				}
			}
		}
	} else {
		arg, succ := args[int8(1)]
		if succ {
			return convertArg(v, reflect.ValueOf(arg))
		}
	}
	return nil
}

func writeArg(body interface{}) (map[interface{}]interface{}, error) {
	args := make(map[interface{}]interface{})
	v := reflect.ValueOf(body)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() == reflect.Struct {
		for i := 0; i < v.NumField(); i++ {
			if token.IsExported(v.Type().Field(i).Name) {
				args[i+1] = v.Field(i).Interface()
			} else {
				return nil, errors.New("must Exported")
			}
		}
		args["n"] = v.NumField()
	} else {
		args[1] = body
		args["n"] = 1
	}
	return args, nil
}

type mpClientCodec struct {
	rwc    io.ReadWriteCloser
	req    map[interface{}]interface{}
	resp   map[interface{}]interface{}
	closed bool
}

func (c *mpClientCodec) WriteRequest(r *Request, body interface{}) (err error) {
	c.req = make(map[interface{}]interface{})
	if body != nil {
		args, err := writeArg(body)
		if err != nil {
			return err
		} else {
			c.req["args"] = args
		}
	}
	c.req["service"] = r.Service
	c.req["func"] = r.Method
	if !r.NoResp {
		c.req["session"] = r.Seq
	}

	b, err := msgpack.Marshal(c.req)
	if err != nil {
		return err
	}

	return writeFrame(c.rwc, b)
}

func (c *mpClientCodec) ReadResponseHeader(r *Response) error {
	b, err := readFrame(c.rwc)
	if err != nil {
		return err
	}

	dec := msgpack.NewDecoder(bytes.NewReader(b))
	dec.SetMapDecoder(func(dec *msgpack.Decoder) (interface{}, error) {
		return dec.DecodeUntypedMap()
	})

	var m interface{}
	err = dec.Decode(&m)
	if err != nil {
		return err
	}

	c.resp = m.(map[interface{}]interface{})

	iSession, succ := c.resp["session"]
	if !succ {
		return errMsg
	}
	if reflect.TypeOf(iSession).ConvertibleTo(reflect.TypeOf(r.Seq)) {
		reflect.ValueOf(&r.Seq).Elem().Set(reflect.ValueOf(iSession).Convert(reflect.TypeOf(r.Seq)))
	} else {
		return errMsg
	}

	iOk, succ := c.resp["ok"]
	if !succ {
		return errMsg
	}
	ok, succ := iOk.(bool)
	if !succ {
		return errMsg
	}
	if !ok {
		iData, succ := c.resp["data"]
		if !succ {
			r.Error = "call error"
			return nil
		}
		data, succ := iData.(string)
		if !succ {
			r.Error = "call error"
			return nil
		}
		r.Error = data
		return nil
	}

	return nil
}

func (c *mpClientCodec) ReadResponseBody(body interface{}) error {
	if body == nil {
		return nil
	}
	v := reflect.ValueOf(body)
	if v.Kind() != reflect.Ptr {
		return errors.New("msgpack: attempt to decode into a non-pointer")
	}

	iData, succ := c.resp["data"]
	if !succ {
		return errMsg
	}
	data, succ := iData.(map[interface{}]interface{})
	if !succ {
		return errMsg
	}

	err := readArg(v, data)
	if err != nil {
		return err
	}

	return nil
}

func (c *mpClientCodec) Close() error {
	if c.closed {
		// Only call c.rwc.Close once; otherwise the semantics are undefined.
		return nil
	}
	c.closed = true
	return c.rwc.Close()
}

type mpServerCodec struct {
	rwc    io.ReadWriteCloser
	req    map[interface{}]interface{}
	resp   map[interface{}]interface{}
	closed bool
}

func (c *mpServerCodec) ReadRequestHeader(r *Request) error {
	b, err := readFrame(c.rwc)
	if err != nil {
		return err
	}

	dec := msgpack.NewDecoder(bytes.NewReader(b))
	dec.SetMapDecoder(func(dec *msgpack.Decoder) (interface{}, error) {
		return dec.DecodeUntypedMap()
	})

	var m interface{}
	err = dec.Decode(&m)
	if err != nil {
		return err
	}

	c.req = m.(map[interface{}]interface{})

	iSession, succ := c.req["session"]
	if !succ {
		r.NoResp = true
	} else {
		if reflect.TypeOf(iSession).ConvertibleTo(reflect.TypeOf(r.Seq)) {
			reflect.ValueOf(&r.Seq).Elem().Set(reflect.ValueOf(iSession).Convert(reflect.TypeOf(r.Seq)))
		} else {
			return errMsg
		}
	}

	iService, succ := c.req["service"]
	if !succ {
		return errMsg
	}
	service, succ := iService.(string)
	if !succ {
		return errMsg
	}
	r.Service = service

	iMethod, succ := c.req["func"]
	if !succ {
		return errMsg
	}
	method, succ := iMethod.(string)
	if !succ {
		return errMsg
	}
	r.Method = method

	return nil
}

func (c *mpServerCodec) ReadRequestBody(body interface{}) error {
	if body == nil {
		return nil
	}

	iArgs, succ := c.req["args"]
	if !succ {
		return nil
	}
	args, succ := iArgs.(map[interface{}]interface{})
	if !succ {
		return errMsg
	}

	v := reflect.ValueOf(body)
	err := readArg(v, args)
	if err != nil {
		return err
	}

	return nil
}

func (c *mpServerCodec) WriteResponse(r *Response, body interface{}) error {
	c.resp = make(map[interface{}]interface{})
	c.resp["session"] = r.Seq
	if r.Error != "" {
		c.resp["ok"] = false
		c.resp["data"] = r.Error
	} else {
		c.resp["ok"] = true
		if body != nil {
			data, err := writeArg(body)
			if err != nil {
				c.resp["ok"] = false
				c.resp["data"] = err.Error()
			} else {
				c.resp["data"] = data
			}
		}
	}

	b, err := msgpack.Marshal(c.resp)
	if err != nil {
		return err
	}

	return writeFrame(c.rwc, b)
}

func (c *mpServerCodec) Close() error {
	if c.closed {
		// Only call c.rwc.Close once; otherwise the semantics are undefined.
		return nil
	}
	c.closed = true
	return c.rwc.Close()
}
