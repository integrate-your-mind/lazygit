package commands

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/go-errors/errors"

	"github.com/jesseduffield/lazygit/pkg/config"
	"github.com/jesseduffield/lazygit/pkg/utils"
	"github.com/mgutz/str"
	"github.com/sirupsen/logrus"
	gitconfig "github.com/tcnksm/go-gitconfig"
)

// Platform stores the os state
type Platform struct {
	os                   string
	catCmd               string
	shell                string
	shellArg             string
	escapedQuote         string
	openCommand          string
	openLinkCommand      string
	fallbackEscapedQuote string
}

// OSCommand holds all the os commands
type OSCommand struct {
	Log                *logrus.Entry
	Platform           *Platform
	Config             config.AppConfigurer
	command            func(string, ...string) *exec.Cmd
	beforeExecuteCmd   func(*exec.Cmd)
	getGlobalGitConfig func(string) (string, error)
	getenv             func(string) string
}

// NewOSCommand os command runner
func NewOSCommand(log *logrus.Entry, config config.AppConfigurer) *OSCommand {
	return &OSCommand{
		Log:                log,
		Platform:           getPlatform(),
		Config:             config,
		command:            exec.Command,
		beforeExecuteCmd:   func(*exec.Cmd) {},
		getGlobalGitConfig: gitconfig.Global,
		getenv:             os.Getenv,
	}
}

// SetCommand sets the command function used by the struct.
// To be used for testing only
func (c *OSCommand) SetCommand(cmd func(string, ...string) *exec.Cmd) {
	c.command = cmd
}

func (c *OSCommand) SetBeforeExecuteCmd(cmd func(*exec.Cmd)) {
	c.beforeExecuteCmd = cmd
}

type RunCommandOptions struct {
	EnvVars []string
}

func (c *OSCommand) RunCommandWithOutputWithOptions(command string, options RunCommandOptions) (string, error) {
	c.Log.WithField("command", command).Info("RunCommand")
	cmd := c.ExecutableFromString(command)
	cmd.Env = append(cmd.Env, options.EnvVars...)
	return sanitisedCommandOutput(cmd.CombinedOutput())
}

func (c *OSCommand) RunCommandWithOptions(command string, options RunCommandOptions) error {
	_, err := c.RunCommandWithOutputWithOptions(command, options)
	return err
}

// RunCommandWithOutput wrapper around commands returning their output and error
// NOTE: If you don't pass any formatArgs we'll just use the command directly,
// however there's a bizarre compiler error/warning when you pass in a formatString
// with a percent sign because it thinks it's supposed to be a formatString when
// in that case it's not. To get around that error you'll need to define the string
// in a variable and pass the variable into RunCommandWithOutput.
func (c *OSCommand) RunCommandWithOutput(formatString string, formatArgs ...interface{}) (string, error) {
	command := formatString
	if formatArgs != nil {
		command = fmt.Sprintf(formatString, formatArgs...)
	}
	c.Log.WithField("command", command).Info("RunCommand")
	cmd := c.ExecutableFromString(command)
	return sanitisedCommandOutput(cmd.CombinedOutput())
}

// RunExecutableWithOutput runs an executable file and returns its output
func (c *OSCommand) RunExecutableWithOutput(cmd *exec.Cmd) (string, error) {
	c.beforeExecuteCmd(cmd)
	return sanitisedCommandOutput(cmd.CombinedOutput())
}

// RunExecutable runs an executable file and returns an error if there was one
func (c *OSCommand) RunExecutable(cmd *exec.Cmd) error {
	_, err := c.RunExecutableWithOutput(cmd)
	return err
}

// ExecutableFromString takes a string like `git status` and returns an executable command for it
func (c *OSCommand) ExecutableFromString(commandStr string) *exec.Cmd {
	splitCmd := str.ToArgv(commandStr)
	cmd := c.command(splitCmd[0], splitCmd[1:]...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	return cmd
}

// RunCommandWithOutputLive runs RunCommandWithOutputLiveWrapper
func (c *OSCommand) RunCommandWithOutputLive(command string, output func(string) string) error {
	return RunCommandWithOutputLiveWrapper(c, command, output)
}

// DetectUnamePass detect a username / password question in a command
// ask is a function that gets executen when this function detect you need to fillin a password
// The ask argument will be "username" or "password" and expects the user's password or username back
func (c *OSCommand) DetectUnamePass(command string, ask func(string) string) error {
	ttyText := ""
	errMessage := c.RunCommandWithOutputLive(command, func(word string) string {
		ttyText = ttyText + " " + word

		prompts := map[string]string{
			`.+'s password:`:         "password",
			`Password\s*for\s*'.+':`: "password",
			`Username\s*for\s*'.+':`: "username",
		}

		for pattern, askFor := range prompts {
			if match, _ := regexp.MatchString(pattern, ttyText); match {
				ttyText = ""
				return ask(askFor)
			}
		}

		return ""
	})
	return errMessage
}

// RunCommand runs a command and just returns the error
func (c *OSCommand) RunCommand(formatString string, formatArgs ...interface{}) error {
	_, err := c.RunCommandWithOutput(formatString, formatArgs...)
	return err
}

// FileType tells us if the file is a file, directory or other
func (c *OSCommand) FileType(path string) string {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return "other"
	}
	if fileInfo.IsDir() {
		return "directory"
	}
	return "file"
}

// RunDirectCommand wrapper around direct commands
func (c *OSCommand) RunDirectCommand(command string) (string, error) {
	c.Log.WithField("command", command).Info("RunDirectCommand")

	return sanitisedCommandOutput(
		c.command(c.Platform.shell, c.Platform.shellArg, command).
			CombinedOutput(),
	)
}

func sanitisedCommandOutput(output []byte, err error) (string, error) {
	outputString := string(output)
	if err != nil {
		// errors like 'exit status 1' are not very useful so we'll create an error
		// from the combined output
		if outputString == "" {
			return "", WrapError(err)
		}
		return outputString, errors.New(outputString)
	}
	return outputString, nil
}

// OpenFile opens a file with the given
func (c *OSCommand) OpenFile(filename string) error {
	commandTemplate := c.Config.GetUserConfig().GetString("os.openCommand")
	templateValues := map[string]string{
		"filename": c.Quote(filename),
	}

	command := utils.ResolvePlaceholderString(commandTemplate, templateValues)
	err := c.RunCommand(command)
	return err
}

// OpenLink opens a file with the given
func (c *OSCommand) OpenLink(link string) error {
	commandTemplate := c.Config.GetUserConfig().GetString("os.openLinkCommand")
	templateValues := map[string]string{
		"link": c.Quote(link),
	}

	command := utils.ResolvePlaceholderString(commandTemplate, templateValues)
	err := c.RunCommand(command)
	return err
}

// EditFile opens a file in a subprocess using whatever editor is available,
// falling back to core.editor, VISUAL, EDITOR, then vi
func (c *OSCommand) EditFile(filename string) (*exec.Cmd, error) {
	editor, _ := c.getGlobalGitConfig("core.editor")

	if editor == "" {
		editor = c.getenv("VISUAL")
	}
	if editor == "" {
		editor = c.getenv("EDITOR")
	}
	if editor == "" {
		if err := c.RunCommand("which vi"); err == nil {
			editor = "vi"
		}
	}
	if editor == "" {
		return nil, errors.New("No editor defined in $VISUAL, $EDITOR, or git config")
	}

	return c.PrepareSubProcess(editor, filename), nil
}

// PrepareSubProcess iniPrepareSubProcessrocess then tells the Gui to switch to it
// TODO: see if this needs to exist, given that ExecutableFromString does the same things
func (c *OSCommand) PrepareSubProcess(cmdName string, commandArgs ...string) *exec.Cmd {
	cmd := c.command(cmdName, commandArgs...)
	if cmd != nil {
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	}
	return cmd
}

// Quote wraps a message in platform-specific quotation marks
func (c *OSCommand) Quote(message string) string {
	message = strings.Replace(message, "`", "\\`", -1)
	escapedQuote := c.Platform.escapedQuote
	if strings.Contains(message, c.Platform.escapedQuote) {
		escapedQuote = c.Platform.fallbackEscapedQuote
	}
	return escapedQuote + message + escapedQuote
}

// Unquote removes wrapping quotations marks if they are present
// this is needed for removing quotes from staged filenames with spaces
func (c *OSCommand) Unquote(message string) string {
	return strings.Replace(message, `"`, "", -1)
}

// AppendLineToFile adds a new line in file
func (c *OSCommand) AppendLineToFile(filename, line string) error {
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return WrapError(err)
	}
	defer f.Close()

	_, err = f.WriteString("\n" + line)
	if err != nil {
		return WrapError(err)
	}
	return nil
}

// CreateTempFile writes a string to a new temp file and returns the file's name
func (c *OSCommand) CreateTempFile(filename, content string) (string, error) {
	tmpfile, err := ioutil.TempFile("", filename)
	if err != nil {
		c.Log.Error(err)
		return "", WrapError(err)
	}

	if _, err := tmpfile.WriteString(content); err != nil {
		c.Log.Error(err)
		return "", WrapError(err)
	}
	if err := tmpfile.Close(); err != nil {
		c.Log.Error(err)
		return "", WrapError(err)
	}

	return tmpfile.Name(), nil
}

// CreateFileWithContent creates a file with the given content
func (c *OSCommand) CreateFileWithContent(path string, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
		c.Log.Error(err)
		return err
	}

	if err := ioutil.WriteFile(path, []byte(content), 0644); err != nil {
		c.Log.Error(err)
		return WrapError(err)
	}

	return nil
}

// Remove removes a file or directory at the specified path
func (c *OSCommand) Remove(filename string) error {
	err := os.RemoveAll(filename)
	return WrapError(err)
}

// FileExists checks whether a file exists at the specified path
func (c *OSCommand) FileExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// RunPreparedCommand takes a pointer to an exec.Cmd and runs it
// this is useful if you need to give your command some environment variables
// before running it
func (c *OSCommand) RunPreparedCommand(cmd *exec.Cmd) error {
	c.beforeExecuteCmd(cmd)
	out, err := cmd.CombinedOutput()
	outString := string(out)
	c.Log.Info(outString)
	if err != nil {
		if len(outString) == 0 {
			return err
		}
		return errors.New(outString)
	}
	return nil
}

// GetLazygitPath returns the path of the currently executed file
func (c *OSCommand) GetLazygitPath() string {
	ex, err := os.Executable() // get the executable path for git to use
	if err != nil {
		ex = os.Args[0] // fallback to the first call argument if needed
	}
	return `"` + filepath.ToSlash(ex) + `"`
}

// RunCustomCommand returns the pointer to a custom command
func (c *OSCommand) RunCustomCommand(command string) *exec.Cmd {
	return c.PrepareSubProcess(c.Platform.shell, c.Platform.shellArg, command)
}

// PipeCommands runs a heap of commands and pipes their inputs/outputs together like A | B | C
func (c *OSCommand) PipeCommands(commandStrings ...string) error {

	cmds := make([]*exec.Cmd, len(commandStrings))

	for i, str := range commandStrings {
		cmds[i] = c.ExecutableFromString(str)
	}

	for i := 0; i < len(cmds)-1; i++ {
		stdout, err := cmds[i].StdoutPipe()
		if err != nil {
			return err
		}

		cmds[i+1].Stdin = stdout
	}

	// keeping this here in case I adapt this code for some other purpose in the future
	// cmds[len(cmds)-1].Stdout = os.Stdout

	finalErrors := []string{}

	wg := sync.WaitGroup{}
	wg.Add(len(cmds))

	for _, cmd := range cmds {
		currentCmd := cmd
		go func() {
			stderr, err := currentCmd.StderrPipe()
			if err != nil {
				c.Log.Error(err)
			}

			if err := currentCmd.Start(); err != nil {
				c.Log.Error(err)
			}

			if b, err := ioutil.ReadAll(stderr); err == nil {
				if len(b) > 0 {
					finalErrors = append(finalErrors, string(b))
				}
			}

			if err := currentCmd.Wait(); err != nil {
				c.Log.Error(err)
			}

			wg.Done()
		}()
	}

	wg.Wait()

	if len(finalErrors) > 0 {
		return errors.New(strings.Join(finalErrors, "\n"))
	}
	return nil
}

func Kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		// somebody got to it before we were able to, poor bastard
		return nil
	}
	return cmd.Process.Kill()
}

func RunLineOutputCmd(cmd *exec.Cmd, onLine func(line string) (bool, error)) error {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Split(bufio.ScanLines)
	if err := cmd.Start(); err != nil {
		return err
	}

	for scanner.Scan() {
		line := scanner.Text()
		stop, err := onLine(line)
		if err != nil {
			return err
		}
		if stop {
			cmd.Process.Kill()
			break
		}
	}

	cmd.Wait()
	return nil
}

func (c *OSCommand) CopyToClipboard(str string) error {
	commandTemplate := c.Config.GetUserConfig().GetString("os.copyToClipboardCommand")
	templateValues := map[string]string{
		"str": c.Quote(str),
	}

	command := utils.ResolvePlaceholderString(commandTemplate, templateValues)

	return c.RunCommand(command)
}
