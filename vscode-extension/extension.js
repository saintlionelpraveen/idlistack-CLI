const vscode = require('vscode');
const path = require('path');
const fs = require('fs');
const os = require('os');
const cp = require('child_process');

/**
 * Resolve the best idlistack binary to use:
 * 1. Check system PATH (if user installed via install.sh or package manager)
 * 2. Fall back to bundled extension bin/idlistack
 * @param {string} extensionPath 
 * @returns {string} Absolute path to executable
 */
function resolveCliPath(extensionPath) {
    const isWin = os.platform() === 'win32';
    const binName = isWin ? 'idlistack.exe' : 'idlistack';

    // 1. Check /usr/local/bin/idlistack directly first (from install.sh)
    if (!isWin) {
        const usrLocalBin = path.join('/usr', 'local', 'bin', binName);
        if (fs.existsSync(usrLocalBin)) {
            return usrLocalBin;
        }
    }

    // 2. Check ~/.local/bin/idlistack directly
    const userLocalBin = path.join(os.homedir(), '.local', 'bin', binName);
    if (fs.existsSync(userLocalBin)) {
        return userLocalBin;
    }

    // 3. Check if idlistack is available on host system PATH
    try {
        const checkCmd = isWin ? `where ${binName}` : `which ${binName}`;
        const stdout = cp.execSync(checkCmd, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
        if (stdout) {
            const firstLine = stdout.split('\n')[0].trim();
            if (fs.existsSync(firstLine)) {
                return firstLine;
            }
        }
    } catch (_) {}

    // 4. Bundled extension binary
    const bundledBin = path.join(extensionPath, 'bin', binName);
    if (fs.existsSync(bundledBin) && !isWin) {
        try {
            fs.chmodSync(bundledBin, 0o755);
        } catch (_) {}
    }
    return bundledBin;
}

/**
 * Reads ~/.idlistack/credentials.json and returns active Keycloak + K3s RBAC session if valid
 */
function getActiveKeycloakSession() {
    try {
        const credPath = path.join(os.homedir(), '.idlistack', 'credentials.json');
        if (!fs.existsSync(credPath)) {
            return null;
        }
        const raw = fs.readFileSync(credPath, 'utf8');
        const creds = JSON.parse(raw);
        if (!creds || !creds.username || !creds.accessToken) {
            return null;
        }
        if (creds.expiresAt && new Date(creds.expiresAt).getTime() < Date.now()) {
            return null;
        }
        return creds;
    } catch (_) {
        return null;
    }
}

/**
 * @param {vscode.ExtensionContext} context
 */
function activate(context) {
    console.log('IdliStack extension v1.9.0 active (Keycloak + K3s RBAC enabled)!');

    const binDir = path.join(context.extensionPath, 'bin');
    const isWin = os.platform() === 'win32';
    const usrLocalBin = '/usr/local/bin';
    const userLocalBin = path.join(os.homedir(), '.local', 'bin');

    // 1. Ensure bundled CLI binary is executable
    try {
        const bundledCli = path.join(binDir, isWin ? 'idlistack.exe' : 'idlistack');
        if (fs.existsSync(bundledCli) && !isWin) {
            fs.chmodSync(bundledCli, 0o755);
        }
    } catch (e) {
        console.error('Failed to set execute permissions on CLI binary:', e);
    }

    // 2. Global Terminal PATH Injection:
    if (context.environmentVariableCollection) {
        context.environmentVariableCollection.prepend('PATH', `${usrLocalBin}${path.delimiter}${userLocalBin}${path.delimiter}${binDir}${path.delimiter}`);
        context.environmentVariableCollection.description = 'IdliStack CLI binary paths';
    }

    // 3. User Environment Registration (~/.local/bin/idlistack & shell rc persistence):
    try {
        const homeDir = os.homedir();
        const localBin = path.join(homeDir, '.local', 'bin');
        if (!fs.existsSync(localBin)) {
            fs.mkdirSync(localBin, { recursive: true });
        }
        const symlinkPath = path.join(localBin, isWin ? 'idlistack.exe' : 'idlistack');
        const cliPath = resolveCliPath(context.extensionPath);
        if (!fs.existsSync(symlinkPath) && fs.existsSync(cliPath)) {
            try {
                fs.symlinkSync(cliPath, symlinkPath);
            } catch (_) {
                fs.copyFileSync(cliPath, symlinkPath);
                if (!isWin) {
                    fs.chmodSync(symlinkPath, 0o755);
                }
            }
        }

        // Ensure ~/.local/bin is present in ~/.bashrc and ~/.zshrc if not already there
        if (!isWin) {
            const exportLine = '\n# IdliStack CLI path\nexport PATH="$HOME/.local/bin:/usr/local/bin:$PATH"\n';
            for (const rcName of ['.bashrc', '.zshrc']) {
                const rcPath = path.join(homeDir, rcName);
                if (fs.existsSync(rcPath)) {
                    const rcContent = fs.readFileSync(rcPath, 'utf8');
                    if (!rcContent.includes('.local/bin')) {
                        fs.appendFileSync(rcPath, exportLine);
                    }
                }
            }
        }
    } catch (e) {
        console.error('Failed to register ~/.local/bin/idlistack:', e);
    }

    function getWorkspaceRoot() {
        const workspaceFolders = vscode.workspace.workspaceFolders;
        if (!workspaceFolders || workspaceFolders.length === 0) {
            vscode.window.showErrorMessage('Please open a workspace folder to use IdliStack.');
            return null;
        }
        return workspaceFolders[0].uri.fsPath;
    }

    // Helper to get or create a dedicated terminal with explicit PATH environment
    function getTerminal(name = 'IdliStack') {
        const customPath = `${usrLocalBin}${path.delimiter}${userLocalBin}${path.delimiter}${binDir}${path.delimiter}${process.env.PATH || ''}`;
        let terminal = vscode.window.terminals.find(t => t.name === name);
        if (!terminal) {
            terminal = vscode.window.createTerminal({
                name: name,
                cwd: getWorkspaceRoot() || undefined,
                env: {
                    PATH: customPath
                }
            });
        }
        terminal.show(true);
        return terminal;
    }

    // Persistent Status Bar Item (reflects Keycloak login & K3s RBAC state)
    const statusBarItem = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
    statusBarItem.command = 'idlistack.menu';

    function updateStatusBar() {
        const session = getActiveKeycloakSession();
        if (session) {
            const scopeLabel = session.scope === 'cluster-wide' ? 'Cluster RBAC' : 'NS RBAC';
            statusBarItem.text = `$(shield) IdliStack (${session.username} • ${scopeLabel})`;
            statusBarItem.tooltip = `IdliStack Authenticated via Keycloak\nUser: ${session.username}\nScope: ${session.scope}\nGroups: ${(session.groups || []).join(', ')}`;
        } else {
            statusBarItem.text = '$(lock) IdliStack (Login Required)';
            statusBarItem.tooltip = 'IdliStack: Click to authenticate with Keycloak & K3s RBAC';
        }
        statusBarItem.show();
    }
    updateStatusBar();
    const statusInterval = setInterval(updateStatusBar, 3000);
    context.subscriptions.push({ dispose: () => clearInterval(statusInterval) });

    /**
     * Checks if user is authenticated with Keycloak before running protected commands.
     * If not logged in, prompts to open the Auth Frontend or Terminal Login.
     */
    async function ensureAuthenticatedBeforeCommand() {
        const session = getActiveKeycloakSession();
        if (session) {
            return true;
        }
        const choice = await vscode.window.showWarningMessage(
            'IdliStack: Keycloak authentication is required before accessing K3s cluster commands.',
            'Login (Web Auth UI)',
            'Login (Terminal)',
            'Deploy Keycloak to K3s'
        );
        if (choice === 'Login (Web Auth UI)') {
            vscode.commands.executeCommand('idlistack.login');
        } else if (choice === 'Login (Terminal)') {
            const cli = resolveCliPath(context.extensionPath);
            const terminal = getTerminal();
            terminal.sendText(`"${cli}" login --cli`);
        } else if (choice === 'Deploy Keycloak to K3s') {
            vscode.commands.executeCommand('idlistack.authSetup');
        }
        return false;
    }

    // Command: idlistack.login (Opens Keycloak Auth Frontend UI)
    let loginDisposable = vscode.commands.registerCommand('idlistack.login', function () {
        const rootPath = getWorkspaceRoot();
        const cli = resolveCliPath(context.extensionPath);
        const terminal = getTerminal();
        if (rootPath) {
            terminal.sendText(`cd "${rootPath}"`);
        }
        terminal.sendText(`"${cli}" login`);
        vscode.window.showInformationMessage('IdliStack: Launching Keycloak & K3s RBAC Authentication Frontend...');
    });

    // Command: idlistack.authSetup (Deploys Keycloak inside K3s idlistack-auth namespace)
    let authSetupDisposable = vscode.commands.registerCommand('idlistack.authSetup', function () {
        const cli = resolveCliPath(context.extensionPath);
        const terminal = getTerminal();
        terminal.sendText(`"${cli}" auth setup`);
        vscode.window.showInformationMessage('IdliStack: Deploying Keycloak inside local K3s cluster (namespace: idlistack-auth)...');
    });

    // Command: idlistack.whoami
    let whoamiDisposable = vscode.commands.registerCommand('idlistack.whoami', function () {
        const cli = resolveCliPath(context.extensionPath);
        const terminal = getTerminal();
        terminal.sendText(`"${cli}" whoami`);
    });

    // Command: idlistack.logout
    let logoutDisposable = vscode.commands.registerCommand('idlistack.logout', function () {
        const cli = resolveCliPath(context.extensionPath);
        const terminal = getTerminal();
        terminal.sendText(`"${cli}" logout`);
        setTimeout(updateStatusBar, 800);
        vscode.window.showInformationMessage('IdliStack: Logged out from Keycloak session.');
    });

    // Command: idlistack.terminal (Opens dedicated terminal with idlistack pre-configured)
    let terminalDisposable = vscode.commands.registerCommand("idlistack.terminal", function () {
        const rootPath = getWorkspaceRoot();
        const customPath = `${usrLocalBin}${path.delimiter}${userLocalBin}${path.delimiter}${binDir}${path.delimiter}${process.env.PATH || ''}`;
        const terminal = vscode.window.createTerminal({
            name: 'IdliStack Terminal',
            cwd: rootPath || undefined,
            env: {
                PATH: customPath
            }
        });
        terminal.show(true);
        terminal.sendText('echo "🚀 IdliStack Terminal ready! Run \\"idlistack login\\", \\"idlistack init\\", \\"idlistack up\\", etc."');
    });

    // Command: idlistack.init
    let initDisposable = vscode.commands.registerCommand("idlistack.init", async function () {
        const rootPath = getWorkspaceRoot();
        if (!rootPath) return;
        if (!(await ensureAuthenticatedBeforeCommand())) return;
        const cli = resolveCliPath(context.extensionPath);
        const terminal = getTerminal();
        terminal.sendText(`cd "${rootPath}"`);
        terminal.sendText(`"${cli}" init`);
    });

    // Command: idlistack.inspect (Inspect stack, version, framework & plan preview)
    let inspectDisposable = vscode.commands.registerCommand("idlistack.inspect", async function () {
        const rootPath = getWorkspaceRoot();
        if (!rootPath) return;
        if (!(await ensureAuthenticatedBeforeCommand())) return;
        const cli = resolveCliPath(context.extensionPath);
        const terminal = getTerminal();
        terminal.sendText(`cd "${rootPath}"`);
        terminal.sendText(`"${cli}" up --inspect`);
        vscode.window.showInformationMessage('IdliStack: Inspecting stack, runtime version, and framework...');
    });

    // Command: idlistack.up (Deploy to K3s)
    let upDisposable = vscode.commands.registerCommand('idlistack.up', async function () {
        const rootPath = getWorkspaceRoot();
        if (!rootPath) return;
        if (!(await ensureAuthenticatedBeforeCommand())) return;

        const cli = resolveCliPath(context.extensionPath);
        const terminal = getTerminal();
        terminal.sendText(`cd "${rootPath}"`);
        
        const configPath = path.join(rootPath, 'idlistack.toml');
        if (!fs.existsSync(configPath)) {
            terminal.sendText(`"${cli}" init && "${cli}" up`);
        } else {
            terminal.sendText(`"${cli}" up`);
        }
        vscode.window.showInformationMessage('IdliStack: Verifying Keycloak RBAC, building OCI image & deploying via Helm to K3s...');
    });

    // Command: idlistack.down (Destroy)
    let downDisposable = vscode.commands.registerCommand('idlistack.down', async function () {
        const rootPath = getWorkspaceRoot();
        if (!rootPath) return;
        if (!(await ensureAuthenticatedBeforeCommand())) return;

        const cli = resolveCliPath(context.extensionPath);
        const terminal = getTerminal();
        terminal.sendText(`cd "${rootPath}"`);
        terminal.sendText(`"${cli}" down`);
    });

    // Command: idlistack.status
    let statusDisposable = vscode.commands.registerCommand('idlistack.status', async function () {
        const rootPath = getWorkspaceRoot();
        if (!rootPath) return;
        if (!(await ensureAuthenticatedBeforeCommand())) return;

        const cli = resolveCliPath(context.extensionPath);
        const terminal = getTerminal();
        terminal.sendText(`cd "${rootPath}"`);
        terminal.sendText(`"${cli}" status`);
    });

    // Command: idlistack.logs
    let logsDisposable = vscode.commands.registerCommand('idlistack.logs', async function () {
        const rootPath = getWorkspaceRoot();
        if (!rootPath) return;
        if (!(await ensureAuthenticatedBeforeCommand())) return;

        const cli = resolveCliPath(context.extensionPath);
        const terminal = getTerminal();
        terminal.sendText(`cd "${rootPath}"`);
        terminal.sendText(`"${cli}" logs`);
    });

    // Status bar quick pick menu
    let menuDisposable = vscode.commands.registerCommand('idlistack.menu', async function () {
        const session = getActiveKeycloakSession();
        const authDesc = session
            ? `Logged in as ${session.username} (${session.scope})`
            : 'Authenticate with Keycloak in K3s (Web Frontend)';

        const items = [
            { label: '$(shield) Login with Keycloak (Auth Frontend)', description: authDesc, cmd: 'idlistack.login' },
            { label: '$(key) Deploy Keycloak Auth Layer to K3s', description: 'Deploy Keycloak inside K3s (idlistack-auth)', cmd: 'idlistack.authSetup' },
            { label: '$(account) Check Auth & RBAC Scope (Whoami)', description: 'Show active Keycloak user, groups & namespaces', cmd: 'idlistack.whoami' },
            { label: '$(cloud-upload) Deploy to K3s', description: 'Verify RBAC, build OCI image & deploy via Helm', cmd: 'idlistack.up' },
            { label: '$(search) Inspect Stack & Plan', description: 'Preview stack, version, framework & build plan', cmd: 'idlistack.inspect' },
            { label: '$(terminal) Open IdliStack Terminal', description: 'Open terminal with idlistack CLI pre-configured', cmd: 'idlistack.terminal' },
            { label: '$(server-environment) Check Status', description: 'Check running pods & launch web dashboard', cmd: 'idlistack.status' },
            { label: '$(terminal) View Logs', description: 'Stream container logs from K3s', cmd: 'idlistack.logs' },
            { label: '$(trash) Destroy Deployment', description: 'Uninstall Helm release from K3s', cmd: 'idlistack.down' },
            { label: '$(sign-out) Logout from Keycloak', description: 'Clear saved Keycloak token & RBAC session', cmd: 'idlistack.logout' }
        ];

        const selection = await vscode.window.showQuickPick(items, {
            placeHolder: session
                ? `IdliStack — Authenticated as ${session.username} (${session.scope.toUpperCase()})`
                : 'IdliStack — Login with Keycloak required before deploying'
        });

        if (selection && selection.cmd) {
            vscode.commands.executeCommand(selection.cmd);
        }
    });

    // Welcome Notification on First Install
    const welcomed = context.globalState.get('idlistack.welcomed_v190');
    if (!welcomed) {
        context.globalState.update('idlistack.welcomed_v190', true);
        vscode.window.showInformationMessage(
            'IdliStack v1.9.0 is ready with Keycloak OIDC & K3s RBAC authentication!',
            'Login with Keycloak',
            'Deploy Keycloak to K3s',
            'Open Terminal'
        ).then(selection => {
            if (selection === 'Login with Keycloak') vscode.commands.executeCommand('idlistack.login');
            if (selection === 'Deploy Keycloak to K3s') vscode.commands.executeCommand('idlistack.authSetup');
            if (selection === 'Open Terminal') vscode.commands.executeCommand('idlistack.terminal');
        });
    }

    context.subscriptions.push(
        loginDisposable,
        authSetupDisposable,
        whoamiDisposable,
        logoutDisposable,
        terminalDisposable,
        initDisposable,
        inspectDisposable,
        upDisposable,
        downDisposable,
        statusDisposable,
        logsDisposable,
        menuDisposable,
        statusBarItem
    );
}

function deactivate() {}

module.exports = {
    activate,
    deactivate
};
