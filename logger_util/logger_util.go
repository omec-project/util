// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

package logger_util

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

type FileHook struct {
	file      *os.File
	flag      int
	chmod     os.FileMode
	formatter *logrus.TextFormatter
}

func NewFileHook(file string, flag int, chmod os.FileMode) (*FileHook, error) {
	plainFormatter := &logrus.TextFormatter{DisableColors: true}
	logFile, err := os.OpenFile(file, flag, chmod)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to write file on filehook %v\n", err)
		return nil, err
	}

	return &FileHook{logFile, flag, chmod, plainFormatter}, nil
}

func CreateFree5gcLogFile(file string) (string, error) {
	// Because free5gc log file will be used by multiple NFs, it is not recommended to rename.
	return createLogFile(file, "", false)
}

func CreateNfLogFile(file string, defaultName string) (string, error) {
	return createLogFile(file, defaultName, true)
}

/*
 * createLogFile
 * @param file, The full file path from arguments input by user.
 * @param defaultName, Default log file name (if it is empty, it means no default log file will be created)
 * @param rename, Modify the file name if the file exists
 * @return error, fullPath
 */
func createLogFile(file string, defaultName string, rename bool) (string, error) {
	var fullPath string
	directory, fileName := filepath.Split(file)

	if directory == "" || fileName == "" {
		directory = "./log/"
		fileName = defaultName
	}

	if fileName == "" {
		return "", nil
	}

	fullPath = filepath.Join(directory, fileName)

	if rename {
		if err := renameOldLogFile(fullPath); err != nil {
			return "", err
		}
	}

	if err := os.MkdirAll(directory, 0775); err != nil {
		return "", fmt.Errorf("make directory %s failed: %v", directory, err)
	}

	sudoUID, errUID := strconv.Atoi(os.Getenv("SUDO_UID"))
	sudoGID, errGID := strconv.Atoi(os.Getenv("SUDO_GID"))
	if errUID == nil && errGID == nil {
		// if using sudo to run the program, errUID will be nil and sudoUID will get the uid who run sudo
		// else errUID will not be nil and sudoUID will be nil
		// If user using sudo to run the program and create log file, log will own by root,
		// here we change own to user so user can view and reuse the file
		if err := os.Chown(directory, sudoUID, sudoGID); err != nil {
			return "", fmt.Errorf("directory %s chown to %d:%d error: %v", directory, sudoUID, sudoGID, err)
		}

		// Create log file or if it already exist, check if user can access it
		if f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666); err != nil {
			// user cannot access it.
			return "", fmt.Errorf("cannot Open %s error: %v", fullPath, err)
		} else {
			// user can access it
			if err := f.Close(); err != nil {
				return "", fmt.Errorf("file %s cannot been closed", fullPath)
			}
			if err := os.Chown(fullPath, sudoUID, sudoGID); err != nil {
				return "", fmt.Errorf("file %s chown to %d:%d error: %v", fullPath, sudoUID, sudoGID, err)
			}
		}
	}

	return fullPath, nil
}

func renameOldLogFile(fullPath string) error {
	_, err := os.Stat(fullPath)

	if os.IsNotExist(err) {
		return nil
	}

	counter := 0
	sep := "."
	fileDir, fileName := filepath.Split(fullPath)

	if contents, err := os.ReadDir(fileDir); err != nil {
		return fmt.Errorf("reads the directory error %v", err)
	} else {
		for _, content := range contents {
			if !content.IsDir() {
				if strings.Contains(content.Name(), (fileName + sep)) {
					counter++
				}
			}
		}
	}

	newFullPath := fmt.Sprintf("%s%s%s%d", fileDir, fileName, sep, (counter + 1))
	if err := os.Rename(fullPath, newFullPath); err != nil {
		return fmt.Errorf("unable to rename file %v", err)
	}

	return nil
}

// Fire event
func (hook *FileHook) Fire(entry *logrus.Entry) error {
	var line string
	if plainformat, err := hook.formatter.Format(entry); err != nil {
		log.Printf("Formatter error: %+v", err)
		return err
	} else {
		line = string(plainformat)
	}
	if _, err := hook.file.WriteString(line); err != nil {
		fmt.Fprintf(os.Stderr, "unable to write file on filehook(entry.String)%v\n", err)
		return err
	}

	return nil
}

func (hook *FileHook) Levels() []logrus.Level {
	return []logrus.Level{
		logrus.PanicLevel,
		logrus.FatalLevel,
		logrus.TraceLevel,
		logrus.ErrorLevel,
		logrus.WarnLevel,
		logrus.InfoLevel,
		logrus.DebugLevel,
	}
}

// The Middleware will write the Gin logs to logrus.
func ginToLogrus(log *logrus.Entry) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		// Process request
		c.Next()

		clientIP := c.ClientIP()
		method := c.Request.Method
		statusCode := c.Writer.Status()
		errorMessage := c.Errors.ByType(gin.ErrorTypePrivate).String()

		if raw != "" {
			path = path + "?" + raw
		}

		log.Infof("| %3d | %15s | %-7s | %s | %s",
			statusCode, clientIP, method, path, errorMessage)
	}
}

// The Middleware will recover the Gin panic to logrus.
func ginRecover(log *logrus.Entry) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if p := recover(); p != nil {
				// Check for a broken connection, as it is not really a condition that warrants a panic stack trace.
				var brokenPipe bool
				if ne, ok := p.(*net.OpError); ok {
					if se, ok := ne.Err.(*os.SyscallError); ok {
						if strings.Contains(strings.ToLower(se.Error()), "broken pipe") ||
							strings.Contains(strings.ToLower(se.Error()), "connection reset by peer") {
							brokenPipe = true
						}
					}
				}

				if log != nil {
					stack := string(debug.Stack())
					if httpRequest, err := httputil.DumpRequest(c.Request, false); err != nil {
						log.Errorf("Dump http request error: %v\n", err)
					} else {
						headers := strings.Split(string(httpRequest), "\r\n")
						for idx, header := range headers {
							current := strings.Split(header, ":")
							if current[0] == "Authorization" {
								headers[idx] = current[0] + ": *"
							}
						}

						// changing Fatalf to Errorf to let program not be exited
						if brokenPipe {
							log.Errorf("%v\n%s", p, string(httpRequest))
						} else if gin.IsDebugging() {
							log.Errorf("[Debugging] panic:\n%s\n%v\n%s", strings.Join(headers, "\r\n"), p, stack)
						} else {
							log.Errorf("panic: %v\n%s", p, stack)
						}
					}
				}

				// If the connection is dead, we can't write a status to it.
				if brokenPipe {
					c.Error(p.(error)) // nolint: errcheck
					c.Abort()
				} else {
					c.AbortWithStatus(http.StatusInternalServerError)
				}
			}
		}()
		c.Next()
	}
}

// NewGinWithLogrus - returns an Engine instance with the ginToLogrus and Recovery middleware already attached.
func NewGinWithLogrus(log *logrus.Entry) *gin.Engine {
	engine := gin.New()
	engine.Use(ginToLogrus(log), ginRecover(log))
	return engine
}
