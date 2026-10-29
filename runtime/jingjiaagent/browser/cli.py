#!/usr/bin/python3
"""Run the bundled Playwright CLI as an unprivileged, task-local browser user."""
import os
from pathlib import Path
import pwd
import shutil
import stat
import subprocess
import sys
import tempfile


ROOT = Path('/opt/jingjiaagent/browser')
WORKSPACE = Path('/workspace')


def prepare_directory(path, uid, gid):
    """Only grant the dedicated browser directories, never the whole workspace."""
    if path.is_symlink():
        raise ValueError(f'browser directory must not be a symlink: {path}')
    path.mkdir(exist_ok=True)
    # Checkpoint extraction recreates files as root/0600. Restore access only
    # inside this managed subtree; physical traversal skips symbolic links.
    if path.stat().st_uid != uid:
        subprocess.run(['/usr/bin/setfacl', '-R', '-P', '-m', f'u:{uid}:rwX', str(path)], check=True)
    os.chown(path, uid, gid)
    os.chmod(path, 0o700)


def positional_indices(arguments):
    """Skip option values as well as flags, including global options after files."""
    values = {'-s', '--session', '--config', '--idle-timeout', '--browser',
              '--device', '--profile', '--filename'}
    positions = []
    index = 0
    while index < len(arguments):
        argument = arguments[index]
        if argument == '--':
            positions.extend(range(index + 1, len(arguments)))
            break
        if argument in values:
            index += 2
        elif argument.startswith('-'):
            index += 1
        else:
            positions.append(index)
            index += 1
    return positions


def stage_script(arguments, work, account):
    """Expose only an explicitly requested workspace script, without chmodding it."""
    options = []
    for index, argument in enumerate(arguments):
        if argument == '--':
            break
        if argument == '--filename':
            if index + 1 >= len(arguments):
                raise ValueError('--filename requires a workspace script')
            options.append((index + 1, arguments[index + 1], False))
        elif argument.startswith('--filename='):
            options.append((index, argument.split('=', 1)[1], True))
    if not options:
        return None
    if len(options) != 1 or not options[0][1]:
        raise ValueError('run-code requires one workspace script')
    index, filename, joined = options[0]
    # Preserve the CLI's fixed working-directory semantics for relative paths.
    source = (work / filename).resolve(strict=True)
    if source == WORKSPACE or not source.is_relative_to(WORKSPACE):
        raise ValueError('script source must be a regular file in /workspace')
    # /tmp is sticky: pwuser cannot replace this root-owned directory while we
    # copy/chown the file. Hand the directory over only after setup completes.
    directory = Path(tempfile.mkdtemp(prefix='jingjiaagent-browser-script-'))
    try:
        destination = directory / 'script.js'
        parent = os.open(WORKSPACE, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            parts = source.relative_to(WORKSPACE).parts
            # Reopen beneath /workspace without following links, so a mutable
            # browser subdirectory cannot redirect the privileged read.
            for part in parts[:-1]:
                child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
                os.close(parent)
                parent = child
            descriptor = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
            with os.fdopen(descriptor, 'rb') as stream:
                if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                    raise ValueError('script source must be a regular file in /workspace')
                with destination.open('xb') as target:
                    shutil.copyfileobj(stream, target)
        finally:
            os.close(parent)
        os.chown(destination, account.pw_uid, account.pw_gid)
        os.chmod(destination, 0o600)
        os.chown(directory, account.pw_uid, account.pw_gid)
        arguments[index] = ('--filename=' if joined else '') + str(destination)
        return directory
    except BaseException:
        shutil.rmtree(directory)
        raise


def main():
    account = pwd.getpwnam('pwuser')
    # Refuse symlinked parents before giving the browser access to output/work.
    for relative in ('.jingjiaagent', '.jingjiaagent/work', '.jingjiaagent/output'):
        path = WORKSPACE / relative
        if path.is_symlink():
            raise ValueError(f'browser directory must not be a symlink: {path}')
        path.mkdir(exist_ok=True)
    # Native workspace directories are private (0700). Grant traversal only to
    # this browser identity; do not make attachments or other task files public.
    if os.geteuid() == 0:
        for relative in ('', '.jingjiaagent', '.jingjiaagent/work', '.jingjiaagent/output'):
            subprocess.run(['/usr/bin/setfacl', '-m', f'u:{account.pw_uid}:--x',
                            str(WORKSPACE / relative)], check=True)
    work = WORKSPACE / '.jingjiaagent/work/browser'
    output = WORKSPACE / '.jingjiaagent/output/browser'
    if os.geteuid() == 0:
        prepare_directory(work, account.pw_uid, account.pw_gid)
        prepare_directory(output, account.pw_uid, account.pw_gid)
    elif os.geteuid() != account.pw_uid:
        raise ValueError('playwright-cli requires the Guest runtime user')

    # The CLI daemon and browser do not need the Agent's model/MCP credentials.
    env = {name: os.environ[name] for name in (
        'LANG', 'LC_ALL', 'TZ', 'SSL_CERT_FILE', 'SSL_CERT_DIR', 'NODE_EXTRA_CA_CERTS',
        'HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'NO_PROXY',
        'http_proxy', 'https_proxy', 'all_proxy', 'no_proxy',
    ) if name in os.environ}
    env.update(PATH='/usr/local/bin:/usr/bin:/bin', HOME=account.pw_dir,
               USER=account.pw_name, LOGNAME=account.pw_name,
               PLAYWRIGHT_BROWSERS_PATH='/opt/jingjiaagent/browsers',
               PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD='1',
               NO_UPDATE_NOTIFIER='1',
               PLAYWRIGHT_CLI_SESSION='work')
    arguments = sys.argv[1:]
    positions = positional_indices(arguments)
    index = positions[0] if positions else None
    command_name = arguments[index] if index is not None else ''
    uploads = []
    if command_name == 'upload' and '--help' not in arguments:
        sources = []
        for position in positions[1:]:
            source = Path(arguments[position]).resolve(strict=True)
            if not source.is_relative_to(WORKSPACE) or not stat.S_ISREG(source.stat().st_mode):
                raise ValueError('upload source must be a regular file in /workspace')
            sources.append((position, source))
        for position, source in sources:
            # Copy only the explicitly selected attachment, without exposing the
            # private attachment tree or changing the original file permissions.
            destination = Path(tempfile.mkdtemp(prefix='upload-', dir=work)) / source.name
            shutil.copyfile(source, destination)
            os.chown(destination, account.pw_uid, account.pw_gid)
            os.chmod(destination, 0o600)
            os.chown(destination.parent, account.pw_uid, account.pw_gid)
            uploads.append(destination)
            arguments[position] = str(destination)
    # --config belongs to open, not to snapshot/click/close. The running session
    # retains it across commands even after a login shell resets the environment.
    if command_name == 'open' and '--help' not in arguments:
        if not any(arg == '--config' or arg.startswith('--config=') for arg in arguments):
            arguments.append('--config=' + str(ROOT / 'config.json'))
        if not any(arg == '--idle-timeout' or arg.startswith('--idle-timeout=') for arg in arguments):
            arguments.append('--idle-timeout=300000')
    script_directory = stage_script(arguments, work, account) if command_name == 'run-code' and '--help' not in arguments else None
    try:
        if os.geteuid() == 0:
            os.setgroups([])
            os.setgid(account.pw_gid)
            os.setuid(account.pw_uid)
        os.chdir(work)
        command = ['/usr/local/bin/node', str(ROOT / 'node_modules/@playwright/cli/playwright-cli.js'), *arguments]
        if uploads or script_directory is not None:
            sys.exit(subprocess.call(command, env=env))
        os.execve(command[0], command, env)
    finally:
        for destination in uploads:
            destination.unlink(missing_ok=True)
            destination.parent.rmdir()
        if script_directory is not None:
            shutil.rmtree(script_directory)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        print(f'Browser setup failed: {error}', file=sys.stderr)
        sys.exit(1)
