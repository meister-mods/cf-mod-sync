using System.Diagnostics;
using System.Text;
using System.Text.Json;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

namespace CfModSync.Client;

public sealed class MainForm : Form
{
    private readonly TextBox serverAddress = new() { Text = "http://127.0.0.1:8080", Dock = DockStyle.Fill };
    private readonly TextBox serverToken = new() { UseSystemPasswordChar = true, Dock = DockStyle.Fill };
    private readonly TextBox minecraftVersion = new() { Text = "1.20.1", Width = 95 };
    private readonly ComboBox channel = new() { DropDownStyle = ComboBoxStyle.DropDownList, Width = 90 };
    private readonly CheckBox dependencies = new() { Text = "필수 의존성 포함", Checked = true, AutoSize = true };
    private readonly CheckBox refresh = new() { Text = "최신 목록 다시 확인", AutoSize = true };
    private readonly TextBox output = new() { Dock = DockStyle.Fill };
    private readonly TextBox browserAddress = new() { Dock = DockStyle.Fill, Text = "https://www.curseforge.com/minecraft/search?class=mc-mods" };
    private readonly CheckBox pickLinks = new() { Text = "모드 링크를 클릭하면 목록에 담기", Checked = true, AutoSize = true };
    private readonly TextBox projectInput = new() { Dock = DockStyle.Fill, PlaceholderText = "CurseForge 모드 URL 붙여넣기" };
    private readonly ListBox basket = new() { Dock = DockStyle.Fill, SelectionMode = SelectionMode.MultiExtended, IntegralHeight = false };
    private readonly Label basketCount = new() { Text = "선택한 모드 · 0개", AutoSize = true, Font = new("Segoe UI", 12, FontStyle.Bold) };
    private readonly Label status = new() { Text = "모드를 목록에 담고 다운로드를 시작해 주세요.", Dock = DockStyle.Fill, AutoEllipsis = true, TextAlign = ContentAlignment.MiddleLeft };
    private readonly TextBox log = new() { Multiline = true, ReadOnly = true, ScrollBars = ScrollBars.Vertical, Dock = DockStyle.Fill, BackColor = Color.White, BorderStyle = BorderStyle.FixedSingle };
    private readonly ProgressBar progress = new() { Dock = DockStyle.Fill, Maximum = 1000 };
    private readonly Button download = MakeButton("선택한 모드 다운로드", 180);
    private readonly Button cancel = MakeButton("취소", 70);
    private readonly WebView2 web = new() { Dock = DockStyle.Fill };
    private readonly string? smokeDirectory;
    private CancellationTokenSource? operation;
    private bool busy;
    private bool customOutput;
    private bool loadingSettings;
    private readonly List<Control> lockedDuringDownload = [];

    private static string AppDirectory => Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "cfmodsync");
    private static string SettingsPath => Path.Combine(AppDirectory, "settings.json");

    public MainForm(string[] args)
    {
        AutoScaleMode = AutoScaleMode.None;
        if (args.Length == 2 && args[0] == "--smoke-test") smokeDirectory = Path.GetFullPath(args[1]);
        Text = "cfmodsync · CurseForge 모드 다운로더";
        Size = new Size(1280, 900);
        MinimumSize = new Size(1060, 760);
        StartPosition = FormStartPosition.CenterScreen;
        Font = new Font("Segoe UI", 10);
        BackColor = Color.FromArgb(244, 246, 249);
        if (smokeDirectory != null)
        {
            ShowInTaskbar = false;
            StartPosition = FormStartPosition.Manual;
            Location = new Point(-20000, -20000);
        }
        channel.Items.AddRange(["all", "beta", "release"]);
        channel.SelectedIndex = 0;
        cancel.Enabled = false;

        var root = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 1, RowCount = 6, Padding = new Padding(18) };
        foreach (var height in new float[] { 64, 45, 42, 44 }) root.RowStyles.Add(new(SizeType.Absolute, height));
        root.RowStyles.Add(new(SizeType.Percent, 100));
        root.RowStyles.Add(new(SizeType.Absolute, 148));
        Controls.Add(root);
        var heading = new FlowLayoutPanel { Dock = DockStyle.Fill, FlowDirection = FlowDirection.TopDown, WrapContents = false, Margin = Padding.Empty };
        heading.Controls.Add(new Label { Text = "cfmodsync", Font = new Font("Segoe UI", 21, FontStyle.Bold), AutoSize = true, Margin = Padding.Empty });
        heading.Controls.Add(new Label { Text = "CurseForge에서 고르고, 서버 캐시에서 받아 보세요.", ForeColor = Color.DimGray, AutoSize = true });
        root.Controls.Add(heading, 0, 0);

        var connection = Table(5);
        connection.ColumnStyles.Add(new(SizeType.Absolute, 75));
        connection.ColumnStyles.Add(new(SizeType.Percent, 58));
        connection.ColumnStyles.Add(new(SizeType.Absolute, 90));
        connection.ColumnStyles.Add(new(SizeType.Percent, 42));
        connection.ColumnStyles.Add(new(SizeType.Absolute, 110));
        connection.Controls.Add(Caption("서버 주소"), 0, 0);
        connection.Controls.Add(serverAddress, 1, 0);
        connection.Controls.Add(Caption("접속 토큰"), 2, 0);
        connection.Controls.Add(serverToken, 3, 0);
        var check = MakeButton("연결 확인", 100);
        check.Click += async (_, _) => await CheckConnectionAsync(check);
        connection.Controls.Add(check, 4, 0);
        root.Controls.Add(connection, 0, 1);
        new ToolTip().SetToolTip(serverToken, "서버의 SERVER_TOKEN입니다. CurseForge API 키는 서버에만 설정합니다. 이 토큰은 디스크에 저장하지 않습니다.");

        var profile = new FlowLayoutPanel { Dock = DockStyle.Fill, WrapContents = false, Margin = Padding.Empty };
        profile.Controls.AddRange([Caption("Minecraft"), minecraftVersion, Caption("Forge  /  채널"), channel, dependencies, refresh]);
        root.Controls.Add(profile, 0, 2);
        var outputRow = Table(3);
        outputRow.ColumnStyles.Add(new(SizeType.Absolute, 75));
        outputRow.ColumnStyles.Add(new(SizeType.Percent, 100));
        outputRow.ColumnStyles.Add(new(SizeType.Absolute, 110));
        outputRow.Controls.Add(Caption("저장 폴더"), 0, 0);
        outputRow.Controls.Add(output, 1, 0);
        var browse = MakeButton("폴더 선택", 100);
        browse.Click += (_, _) =>
        {
            using var dialog = new FolderBrowserDialog { Description = "JAR 파일을 저장할 폴더", UseDescriptionForTitle = true, SelectedPath = output.Text };
            if (dialog.ShowDialog(this) == DialogResult.OK) { customOutput = true; output.Text = dialog.SelectedPath; }
        };
        output.TextChanged += (_, _) => { if (!loadingSettings && output.Focused) customOutput = true; };
        minecraftVersion.TextChanged += (_, _) => { if (!loadingSettings && !customOutput) SetDefaultOutput(); };
        outputRow.Controls.Add(browse, 2, 0);
        root.Controls.Add(outputRow, 0, 3);

        var split = new SplitContainer
        {
            Dock = DockStyle.Fill, Size = new Size(1200, 500), SplitterDistance = 760,
            Panel1MinSize = 550, Panel2MinSize = 330, SplitterWidth = 12, BackColor = BackColor
        };
        split.Panel1.Controls.Add(BuildBrowser());
        split.Panel2.Controls.Add(BuildBasket());
        root.Controls.Add(split, 0, 4);
        root.Controls.Add(BuildFooter(), 0, 5);
        lockedDuringDownload.AddRange([serverAddress, serverToken, check, minecraftVersion, channel, dependencies, refresh, output, browse]);
        LoadSettings();
        if (args.Length == 1 && File.Exists(args[0])) ImportFile(args[0]);
        Shown += async (_, _) => await InitializeBrowserAsync();
        FormClosing += (_, e) =>
        {
            if (busy)
            {
                operation?.Cancel();
                e.Cancel = true;
                WriteLog("작업을 취소하고 있습니다. 취소가 끝나면 창을 닫으실 수 있습니다.");
                return;
            }
            SaveSettings();
        };
        AutoScaleDimensions = new SizeF(96F, 96F);
        AutoScaleMode = AutoScaleMode.Dpi;
    }

    private static Button MakeButton(string text, int width) => new()
    {
        Text = text, Width = width, Height = 32, FlatStyle = FlatStyle.System, AutoSize = false,
        Margin = new Padding(3)
    };
    private static Label Caption(string text) => new()
    {
        Text = text, AutoSize = true, TextAlign = ContentAlignment.MiddleLeft, Margin = new Padding(3, 8, 6, 0)
    };
    private static TableLayoutPanel Table(int columns) => new() { Dock = DockStyle.Fill, ColumnCount = columns, RowCount = 1, Margin = Padding.Empty };

    protected override void OnLoad(EventArgs e)
    {
        // Controls are built in 96-DPI design units. Apply the initial scale
        // after every nested layout exists, then let WinForms track DPI moves.
        AutoScaleDimensions = new SizeF(96F, 96F);
        PerformAutoScale();
        base.OnLoad(e);
    }

    private Control BuildBrowser()
    {
        var panel = new TableLayoutPanel { Dock = DockStyle.Fill, RowCount = 3, ColumnCount = 1, BackColor = Color.White, Padding = new Padding(8) };
        panel.RowStyles.Add(new(SizeType.Absolute, 40));
        panel.RowStyles.Add(new(SizeType.Absolute, 38));
        panel.RowStyles.Add(new(SizeType.Percent, 100));
        var navigation = Table(5);
        navigation.ColumnStyles.Add(new(SizeType.Absolute, 40));
        navigation.ColumnStyles.Add(new(SizeType.Absolute, 40));
        navigation.ColumnStyles.Add(new(SizeType.Absolute, 48));
        navigation.ColumnStyles.Add(new(SizeType.Percent, 100));
        navigation.ColumnStyles.Add(new(SizeType.Absolute, 56));
        var back = MakeButton("‹", 34);
        var forward = MakeButton("›", 34);
        var reload = MakeButton("↻", 42);
        var go = MakeButton("이동", 50);
        back.Click += (_, _) => { if (web.CanGoBack) web.GoBack(); };
        forward.Click += (_, _) => { if (web.CanGoForward) web.GoForward(); };
        reload.Click += (_, _) => web.CoreWebView2?.Reload();
        go.Click += (_, _) => Navigate(browserAddress.Text);
        browserAddress.KeyDown += (_, e) => { if (e.KeyCode == Keys.Enter) { e.SuppressKeyPress = true; Navigate(browserAddress.Text); } };
        navigation.Controls.Add(back, 0, 0);
        navigation.Controls.Add(forward, 1, 0);
        navigation.Controls.Add(reload, 2, 0);
        navigation.Controls.Add(browserAddress, 3, 0);
        navigation.Controls.Add(go, 4, 0);
        panel.Controls.Add(navigation, 0, 0);
        var picker = new FlowLayoutPanel { Dock = DockStyle.Fill, WrapContents = false, Margin = Padding.Empty };
        var addCurrent = MakeButton("현재 프로젝트 담기", 160);
        addCurrent.Click += (_, _) => AddUrl(web.Source?.AbsoluteUri ?? browserAddress.Text);
        picker.Controls.AddRange([pickLinks, addCurrent]);
        panel.Controls.Add(picker, 0, 1);
        panel.Controls.Add(web, 0, 2);
        return panel;
    }

    private Control BuildBasket()
    {
        var panel = new TableLayoutPanel { Dock = DockStyle.Fill, RowCount = 5, ColumnCount = 1, BackColor = Color.White, Padding = new Padding(10) };
        panel.RowStyles.Add(new(SizeType.Absolute, 34));
        panel.RowStyles.Add(new(SizeType.Absolute, 42));
        panel.RowStyles.Add(new(SizeType.Absolute, 42));
        panel.RowStyles.Add(new(SizeType.Percent, 100));
        panel.RowStyles.Add(new(SizeType.Absolute, 42));
        panel.Controls.Add(basketCount, 0, 0);
        var addRow = Table(2);
        addRow.ColumnStyles.Add(new(SizeType.Percent, 100));
        addRow.ColumnStyles.Add(new(SizeType.Absolute, 62));
        var add = MakeButton("담기", 56);
        add.Click += (_, _) => { if (AddUrl(projectInput.Text)) projectInput.Clear(); };
        projectInput.KeyDown += (_, e) => { if (e.KeyCode == Keys.Enter) { e.SuppressKeyPress = true; add.PerformClick(); } };
        addRow.Controls.Add(projectInput, 0, 0);
        addRow.Controls.Add(add, 1, 0);
        panel.Controls.Add(addRow, 0, 1);
        var files = new FlowLayoutPanel { Dock = DockStyle.Fill, WrapContents = false, Margin = Padding.Empty };
        var import = MakeButton("목록 가져오기", 128);
        var export = MakeButton("목록 저장", 105);
        import.Click += (_, _) =>
        {
            using var dialog = new OpenFileDialog { Filter = "모드 목록|.mods;.mods.example;*.mods;*.txt;*.list|모든 파일|*.*", CheckFileExists = true };
            if (dialog.ShowDialog(this) == DialogResult.OK) ImportFile(dialog.FileName);
        };
        export.Click += (_, _) =>
        {
            using var dialog = new SaveFileDialog { Filter = "모드 목록|*.*", FileName = ".mods", AddExtension = false };
            if (dialog.ShowDialog(this) != DialogResult.OK) return;
            try { File.WriteAllLines(dialog.FileName, basket.Items.Cast<ProjectItem>().Select(x => x.Url), new UTF8Encoding(false)); WriteLog("모드 목록을 저장했습니다."); }
            catch (Exception error) { WriteLog("목록 저장 실패: " + error.Message); }
        };
        files.Controls.AddRange([import, export]);
        panel.Controls.Add(files, 0, 2);
        panel.Controls.Add(basket, 0, 3);
        var edit = new FlowLayoutPanel { Dock = DockStyle.Fill, WrapContents = false, Margin = Padding.Empty };
        var remove = MakeButton("선택 제거", 105);
        var clear = MakeButton("모두 비우기", 110);
        remove.Click += (_, _) => { foreach (var index in basket.SelectedIndices.Cast<int>().OrderDescending()) basket.Items.RemoveAt(index); UpdateCount(); };
        clear.Click += (_, _) => { basket.Items.Clear(); UpdateCount(); };
        edit.Controls.AddRange([remove, clear]);
        panel.Controls.Add(edit, 0, 4);
        return panel;
    }

    private Control BuildFooter()
    {
        var panel = new TableLayoutPanel { Dock = DockStyle.Fill, RowCount = 3, ColumnCount = 1, Padding = new Padding(0, 8, 0, 0) };
        panel.RowStyles.Add(new(SizeType.Absolute, 40));
        panel.RowStyles.Add(new(SizeType.Absolute, 16));
        panel.RowStyles.Add(new(SizeType.Percent, 100));
        var actions = Table(4);
        actions.ColumnStyles.Add(new(SizeType.Percent, 100));
        actions.ColumnStyles.Add(new(SizeType.Absolute, 118));
        actions.ColumnStyles.Add(new(SizeType.Absolute, 188));
        actions.ColumnStyles.Add(new(SizeType.Absolute, 78));
        actions.Controls.Add(status, 0, 0);
        var open = MakeButton("저장 폴더 열기", 112);
        open.Click += (_, _) =>
        {
            try { Directory.CreateDirectory(output.Text); Process.Start(new ProcessStartInfo("explorer.exe", output.Text) { UseShellExecute = true }); }
            catch (Exception error) { WriteLog(error.Message); }
        };
        download.Click += async (_, _) => await DownloadAsync();
        cancel.Click += (_, _) => operation?.Cancel();
        actions.Controls.Add(open, 1, 0);
        actions.Controls.Add(download, 2, 0);
        actions.Controls.Add(cancel, 3, 0);
        panel.Controls.Add(actions, 0, 0);
        panel.Controls.Add(progress, 0, 1);
        panel.Controls.Add(log, 0, 2);
        return panel;
    }

    private async Task InitializeBrowserAsync()
    {
        var ready = false;
        var navigation = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        var navigationError = "";
        try
        {
            var profile = Path.Combine(smokeDirectory ?? AppDirectory, "webview");
            var environment = await CoreWebView2Environment.CreateAsync(userDataFolder: profile);
            await web.EnsureCoreWebView2Async(environment).WaitAsync(TimeSpan.FromSeconds(30));
            web.CoreWebView2.Settings.AreHostObjectsAllowed = false;
            web.CoreWebView2.Settings.IsWebMessageEnabled = false;
            web.CoreWebView2.PermissionRequested += (_, e) => e.State = CoreWebView2PermissionState.Deny;
            web.CoreWebView2.DownloadStarting += (_, e) => { e.Cancel = true; WriteLog("프로젝트를 목록에 담은 뒤 서버 다운로드를 이용해 주세요."); };
            web.CoreWebView2.NavigationStarting += (_, e) =>
            {
                if (!ProjectUrls.IsCurseForgePage(e.Uri)) { e.Cancel = true; WriteLog("웹뷰에서는 CurseForge HTTPS 페이지만 열 수 있습니다."); return; }
                if (pickLinks.Checked)
                {
                    try { var url = ProjectUrls.Normalize(e.Uri); e.Cancel = true; AddUrl(url); }
                    catch (FormatException) { }
                }
            };
            web.CoreWebView2.NewWindowRequested += (_, e) =>
            {
                e.Handled = true;
                if (!ProjectUrls.IsCurseForgePage(e.Uri)) return;
                if (pickLinks.Checked)
                {
                    try { AddUrl(ProjectUrls.Normalize(e.Uri)); return; }
                    catch (FormatException) { }
                }
                web.CoreWebView2.Navigate(e.Uri);
            };
            web.CoreWebView2.SourceChanged += (_, _) => browserAddress.Text = web.Source?.AbsoluteUri ?? "";
            web.CoreWebView2.NavigationCompleted += (_, e) =>
            {
                navigationError = e.WebErrorStatus.ToString();
                navigation.TrySetResult(e.IsSuccess);
            };
            web.CoreWebView2.Navigate(browserAddress.Text);
            ready = true;
        }
        catch (Exception error)
        {
            WriteLog("웹뷰 시작 실패: " + error.Message + " · Microsoft Edge WebView2 Runtime을 확인해 주세요. 파일 가져오기와 URL 입력은 계속 사용할 수 있습니다.");
        }
        if (smokeDirectory != null)
        {
            await Task.WhenAny(navigation.Task, Task.Delay(15000));
            await Task.Delay(1000);
            Directory.CreateDirectory(smokeDirectory);
            using var bitmap = new Bitmap(Width, Height);
            DrawToBitmap(bitmap, new Rectangle(Point.Empty, Size));
            bitmap.Save(Path.Combine(smokeDirectory, "client.png"));
            if (ready)
            {
                await using var capture = File.Create(Path.Combine(smokeDirectory, "webview.png"));
                await web.CoreWebView2.CapturePreviewAsync(CoreWebView2CapturePreviewImageFormat.Png, capture);
            }
            File.WriteAllText(Path.Combine(smokeDirectory, "result.json"), JsonSerializer.Serialize(new
            {
                webview_ready = ready, navigation_succeeded = navigation.Task.IsCompletedSuccessfully && navigation.Task.Result,
                navigation_error = navigationError, width = Width, height = Height, dpi = DeviceDpi
            }));
            Environment.ExitCode = ready ? 0 : 1;
            Close();
        }
    }

    private void Navigate(string url)
    {
        if (!ProjectUrls.IsCurseForgePage(url)) { WriteLog("CurseForge HTTPS 주소를 입력해 주세요."); return; }
        web.CoreWebView2?.Navigate(url);
    }

    private bool AddUrl(string raw)
    {
        try
        {
            var url = ProjectUrls.Normalize(raw);
            if (basket.Items.Cast<ProjectItem>().Any(x => x.Url == url)) { status.Text = "이미 담은 모드입니다."; return true; }
            if (basket.Items.Count >= 500) throw new FormatException("한 번에 최대 500개의 프로젝트를 담을 수 있습니다.");
            basket.Items.Add(new ProjectItem(url));
            UpdateCount();
            status.Text = $"목록에 추가했습니다: {url.Split('/')[^1]}";
            return true;
        }
        catch (FormatException error) { WriteLog(error.Message); return false; }
    }

    private void ImportFile(string path)
    {
        try
        {
            var urls = ProjectUrls.ParseList(File.ReadLines(path));
            var count = basket.Items.Cast<ProjectItem>().Select(x => x.Url).Union(urls).Count();
            if (count > 500) throw new FormatException("모드 목록은 최대 500개까지 담을 수 있습니다.");
            foreach (var url in urls) AddUrl(url);
            WriteLog($"{Path.GetFileName(path)}에서 {urls.Length}개 프로젝트를 가져왔습니다.");
        }
        catch (Exception error) { WriteLog("목록 가져오기 실패: " + error.Message); }
    }

    private void UpdateCount() => basketCount.Text = $"선택한 모드 · {basket.Items.Count}개";
    private void WriteLog(string message)
    {
        status.Text = message;
        log.AppendText($"[{DateTime.Now:HH:mm:ss}] {message}{Environment.NewLine}");
    }

    private async Task CheckConnectionAsync(Button button)
    {
        button.Enabled = false;
        try
        {
            using var api = new ServerClient(serverAddress.Text, serverToken.Text);
            using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(15));
            await api.CheckAsync(timeout.Token);
            WriteLog("서버에 연결했습니다. Forge 모드 다운로드를 요청할 수 있습니다.");
        }
        catch (Exception error) { WriteLog("연결 확인 실패: " + error.Message); }
        finally { if (!busy) button.Enabled = true; }
    }

    private async Task DownloadAsync()
    {
        if (busy) return;
        if (basket.Items.Count == 0) { WriteLog("먼저 모드를 목록에 담아 주세요."); return; }
        using var cancellation = new CancellationTokenSource();
        operation = cancellation;
        busy = true;
        download.Enabled = false;
        cancel.Enabled = true;
        foreach (var control in lockedDuringDownload) control.Enabled = false;
        progress.Value = 0;
        ServerClient? api = null;
        string? jobId = null;
        var terminal = false;
        try
        {
            var directory = Path.GetFullPath(output.Text);
            if (directory == Path.GetPathRoot(directory)) throw new IOException("별도의 다운로드 폴더를 선택해 주세요.");
            api = new ServerClient(serverAddress.Text, serverToken.Text);
            var request = new JobRequest(basket.Items.Cast<ProjectItem>().Select(x => x.Url).ToArray(),
                minecraftVersion.Text.Trim(), channel.Text, dependencies.Checked, refresh.Checked);
            SaveSettings();
            WriteLog("서버에 모드 목록을 전달했습니다.");
            var job = await api.CreateJobAsync(request, cancellation.Token);
            jobId = job.Id;
            var issues = new List<JobIssue>();
            var localFiles = new List<JarFile>();
            var transferred = new HashSet<string>(StringComparer.Ordinal);
            while (true)
            {
                terminal = job.IsTerminal;
                if (job.State == "cancelled") throw new OperationCanceledException("서버 작업이 취소되었습니다.");
                // Receive each ready file while the server fills other cache
                // entries. A cache hit need not wait for the entire batch.
                var ready = job.Files.Where(x => x.Verified != null && x.DownloadPath != "" &&
                    x.Status is "cached" or "downloaded").OrderByDescending(x => x.Status == "cached");
                foreach (var file in ready)
                {
                    if (!transferred.Add(file.DownloadPath)) continue;
                    cancellation.Token.ThrowIfCancellationRequested();
                    var localCount = localFiles.Count;
                    var snapshot = job;
                    var reporter = new Progress<TransferProgress>(value =>
                    {
                        progress.Value = Math.Clamp((int)((snapshot.Completed + localCount + (double)value.Received / value.Total) * 500 / Math.Max(1, snapshot.Total)), 0, 1000);
                        status.Text = $"받는 중 {localCount + 1}/{snapshot.Total}: {value.Filename} · {value.Received / 1048576.0:F1}/{value.Total / 1048576.0:F1} MiB";
                    });
                    try
                    {
                        var result = await api.DownloadAsync(file, directory, reporter, cancellation.Token);
                        localFiles.Add(file with { Status = result });
                        WriteLog($"{(result == "cached" ? "로컬 재사용" : "다운로드 완료")}: {file.Filename}");
                    }
                    catch (Exception error) when (error is IOException or HttpRequestException or UnauthorizedAccessException)
                    {
                        issues.Add(new JobIssue(file.Slug, error.Message));
                        localFiles.Add(file with { Status = "failed", Verified = null, Error = error.Message });
                        WriteLog($"파일 다운로드 실패: {file.Filename} · {error.Message}");
                    }
                }
                if (terminal) break;
                status.Text = job.State is "queued" or "resolving" ? "서버에서 호환 파일과 의존성을 확인하고 있습니다." :
                    $"서버 캐시 준비: {job.Completed}/{job.Total} · 수신 완료 {localFiles.Count}개 · 캐시 재사용 {job.CacheHits}개";
                progress.Value = job.Total == 0 ? 0 : Math.Clamp((job.Completed + localFiles.Count) * 500 / job.Total, 0, 1000);
                await Task.Delay(600, cancellation.Token);
                job = await api.GetJobAsync(job.Id, cancellation.Token);
            }
            issues.AddRange(job.Errors);
            foreach (var issue in job.Errors) WriteLog($"{issue.Project}: {issue.Error}");
            // Preserve failed server selections in the local manifest as well.
            localFiles.AddRange(job.Files.Where(x => x.Status == "failed"));
            await ServerClient.WriteManifestAsync(directory, job, localFiles.ToArray(), issues.ToArray(), cancellation.Token);
            progress.Value = 1000;
            WriteLog(issues.Count == 0 ? $"완료: {localFiles.Count}개 JAR을 검증하고 저장했습니다." :
                $"일부 항목 실패: {issues.Count}건 · 저장 폴더의 manifest.json과 로그를 확인해 주세요.");
        }
        catch (OperationCanceledException)
        {
            WriteLog("다운로드를 취소했습니다. 검증이 끝난 파일은 저장 폴더에 유지됩니다.");
            if (!terminal && api != null && jobId != null)
            {
                try { using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(5)); await api.CancelJobAsync(jobId, timeout.Token); }
                catch (Exception) { }
            }
        }
        catch (Exception error) { WriteLog("다운로드 실패: " + error.Message); }
        finally
        {
            api?.Dispose();
            operation = null;
            busy = false;
            download.Enabled = true;
            cancel.Enabled = false;
            foreach (var control in lockedDuringDownload) control.Enabled = true;
        }
    }

    private void SetDefaultOutput() => output.Text = Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.MyDocuments), "cfmodsync", "downloads",
        minecraftVersion.Text.Trim() + "-forge");

    private void LoadSettings()
    {
        loadingSettings = true;
        try
        {
            SetDefaultOutput();
            if (smokeDirectory != null || !File.Exists(SettingsPath)) return;
            var settings = JsonSerializer.Deserialize<ClientSettings>(File.ReadAllText(SettingsPath), Protocol.Json);
            if (settings == null) return;
            serverAddress.Text = settings.ServerAddress;
            minecraftVersion.Text = settings.MinecraftVersion;
            channel.SelectedItem = settings.Channel is "all" or "beta" or "release" ? settings.Channel : "all";
            dependencies.Checked = settings.Dependencies;
            customOutput = settings.CustomOutput;
            if (customOutput) output.Text = settings.Output;
            else SetDefaultOutput();
            foreach (var url in settings.ProjectUrls.Take(500)) AddUrl(url);
        }
        catch (Exception error) { WriteLog("저장된 설정을 불러오지 못했습니다: " + error.Message); }
        finally { loadingSettings = false; }
    }

    private void SaveSettings()
    {
        if (smokeDirectory != null) return;
        try
        {
            Directory.CreateDirectory(AppDirectory);
            var settings = new ClientSettings(serverAddress.Text, minecraftVersion.Text, channel.Text,
                dependencies.Checked, output.Text, customOutput, basket.Items.Cast<ProjectItem>().Select(x => x.Url).ToArray());
            File.WriteAllText(SettingsPath, JsonSerializer.Serialize(settings, Protocol.Json));
        }
        catch (Exception error) { WriteLog("설정 저장 실패: " + error.Message); }
    }

    private sealed record ProjectItem(string Url)
    {
        public override string ToString() => Url.Split('/')[^1];
    }
    private sealed record ClientSettings(string ServerAddress, string MinecraftVersion, string Channel,
        bool Dependencies, string Output, bool CustomOutput, string[] ProjectUrls);
}
