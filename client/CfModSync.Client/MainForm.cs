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
    private readonly ComboBox forgeVersion = new() { Width = 110, DropDownStyle = ComboBoxStyle.DropDown, MaxLength = 32 };
    private readonly TextBox installedFolder = new() { Dock = DockStyle.Fill, PlaceholderText = "Optional: existing mods folder for comparison (read only)" };
    private readonly CheckBox allowReviewedChanges = new() { Text = "Allow reviewed replacements", AutoSize = true };
    private readonly ToolTip tips = new();
    private readonly TabControl basketTabs = new() { Dock = DockStyle.Fill };
    private readonly TabPage reportTab = new("Validation report");
    private readonly DataGridView reportGrid = new()
    {
        Dock = DockStyle.Fill, ReadOnly = true, AllowUserToAddRows = false, AllowUserToDeleteRows = false,
        RowHeadersVisible = false, AutoSizeColumnsMode = DataGridViewAutoSizeColumnsMode.Fill,
        AutoSizeRowsMode = DataGridViewAutoSizeRowsMode.AllCells, BackgroundColor = Color.White,
        BorderStyle = BorderStyle.None, SelectionMode = DataGridViewSelectionMode.FullRowSelect
    };
    private readonly ComboBox channel = new() { DropDownStyle = ComboBoxStyle.DropDownList, Width = 90 };
    private readonly CheckBox dependencies = new() { Text = "Include required dependencies", Checked = true, AutoSize = true };
    private readonly CheckBox refresh = new() { Text = "Refresh latest files", AutoSize = true };
    private readonly TextBox output = new() { Dock = DockStyle.Fill };
    private readonly TextBox browserAddress = new() { Dock = DockStyle.Fill, Text = "https://www.curseforge.com/minecraft/search?class=mc-mods" };
    private readonly CheckBox pickLinks = new() { Text = "Add mod links when clicked", Checked = true, AutoSize = true };
    private readonly TextBox projectInput = new() { Dock = DockStyle.Fill, PlaceholderText = "Paste a CurseForge mod URL" };
    private readonly ListBox basket = new() { Dock = DockStyle.Fill, SelectionMode = SelectionMode.MultiExtended, IntegralHeight = false, Sorted = true };
    private readonly Label basketCount = new() { Text = "Selected mods · 0", AutoSize = true, Font = new("Segoe UI", 12, FontStyle.Bold) };
    private readonly Label status = new() { Text = "Add mods to the list, then start downloading.", Dock = DockStyle.Fill, AutoEllipsis = true, TextAlign = ContentAlignment.MiddleLeft };
    private readonly TextBox log = new() { Multiline = true, ReadOnly = true, ScrollBars = ScrollBars.Vertical, Dock = DockStyle.Fill, BackColor = Color.White, BorderStyle = BorderStyle.FixedSingle };
    private readonly ProgressBar progress = new() { Dock = DockStyle.Fill, Maximum = 1000 };
    private readonly Button download = MakeButton("Download selected mods", 180);
    private readonly Button cancel = MakeButton("Cancel", 70);
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
        Text = "cfmodsync · CurseForge Mod Downloader";
        Size = new Size(1320, 940);
        MinimumSize = new Size(1100, 820);
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
        channel.SelectedItem = "release";
        cancel.Enabled = false;
        foreach (var control in new Control[] { serverAddress, serverToken, minecraftVersion, forgeVersion, channel, output, installedFolder, browserAddress, projectInput })
        {
            control.Dock = DockStyle.None;
            control.Anchor = AnchorStyles.Left | AnchorStyles.Right;
        }

        var root = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 1, RowCount = 8, Padding = new Padding(18) };
        root.ColumnStyles.Add(new(SizeType.Percent, 100));
        foreach (var height in new float[] { 64, 44, 42, 44, 44, 44 }) root.RowStyles.Add(new(SizeType.Absolute, height));
        root.RowStyles.Add(new(SizeType.Percent, 100));
        root.RowStyles.Add(new(SizeType.Absolute, 148));
        Controls.Add(root);
        var heading = new FlowLayoutPanel { Dock = DockStyle.Fill, FlowDirection = FlowDirection.TopDown, WrapContents = false, Margin = Padding.Empty };
        heading.Controls.Add(new Label { Text = "cfmodsync", Font = new Font("Segoe UI", 21, FontStyle.Bold), AutoSize = true, Margin = Padding.Empty });
        heading.Controls.Add(new Label { Text = "Select your Forge version. Review compatibility before downloading.", ForeColor = Color.DimGray, AutoSize = true });
        root.Controls.Add(heading, 0, 0);

        var connection = Table(5);
        connection.ColumnStyles.Add(new(SizeType.Absolute, 120));
        connection.ColumnStyles.Add(new(SizeType.Percent, 58));
        connection.ColumnStyles.Add(new(SizeType.Absolute, 110));
        connection.ColumnStyles.Add(new(SizeType.Percent, 42));
        connection.ColumnStyles.Add(new(SizeType.Absolute, 110));
        connection.Controls.Add(Caption("Server"), 0, 0);
        connection.Controls.Add(serverAddress, 1, 0);
        connection.Controls.Add(Caption("Access token"), 2, 0);
        connection.Controls.Add(serverToken, 3, 0);
        var check = MakeButton("Connect", 100);
        check.Click += async (_, _) => await CheckConnectionAsync(check);
        connection.Controls.Add(check, 4, 0);
        root.Controls.Add(connection, 0, 1);
        tips.SetToolTip(serverToken, "Enter the server's SERVER_TOKEN. Keep the CurseForge API key on the server. This token is not saved.");
        tips.SetToolTip(forgeVersion, "Enter the exact installed Forge version. The server inspects JAR requirements and selects the highest compatible mod versions.");
        tips.SetToolTip(allowReviewedChanges, "Use only after reviewing missing mods, downgrades, and replacements in the Validation report. This approval applies to one request.");
        tips.SetToolTip(channel, "release: stable API releases only. beta: release and beta. all: includes alpha. A pinned file is an explicit override.");

        var profile = Table(7);
        foreach (var width in new float[] { 120, 110, 110, 110, 110, 110 }) profile.ColumnStyles.Add(new(SizeType.Absolute, width));
        profile.ColumnStyles.Add(new(SizeType.Percent, 100));
        profile.Controls.Add(Caption("Minecraft"), 0, 0);
        profile.Controls.Add(minecraftVersion, 1, 0);
        profile.Controls.Add(Caption("Forge version"), 2, 0);
        profile.Controls.Add(forgeVersion, 3, 0);
        profile.Controls.Add(Caption("Release policy"), 4, 0);
        profile.Controls.Add(channel, 5, 0);
        profile.Controls.Add(new Label { Text = "Highest compatible versions", Dock = DockStyle.Fill, TextAlign = ContentAlignment.MiddleLeft, ForeColor = Color.DimGray, AutoEllipsis = true }, 6, 0);
        root.Controls.Add(profile, 0, 2);
        var outputRow = Table(3);
        outputRow.ColumnStyles.Add(new(SizeType.Absolute, 120));
        outputRow.ColumnStyles.Add(new(SizeType.Percent, 100));
        outputRow.ColumnStyles.Add(new(SizeType.Absolute, 110));
        outputRow.Controls.Add(Caption("Save to"), 0, 0);
        outputRow.Controls.Add(output, 1, 0);
        var browse = MakeButton("Browse...", 100);
        browse.Click += (_, _) =>
        {
            using var dialog = new FolderBrowserDialog { Description = "Choose a folder for JAR files", UseDescriptionForTitle = true, SelectedPath = output.Text };
            if (dialog.ShowDialog(this) == DialogResult.OK) { customOutput = true; output.Text = dialog.SelectedPath; }
        };
        output.TextChanged += (_, _) => { if (!loadingSettings && output.Focused) customOutput = true; };
        minecraftVersion.TextChanged += (_, _) =>
        {
            if (!loadingSettings) { forgeVersion.Text = ""; PopulateForgeVersions(); if (!customOutput) SetDefaultOutput(); }
        };
        forgeVersion.TextChanged += (_, _) => { if (!loadingSettings && !customOutput) SetDefaultOutput(); };
        outputRow.Controls.Add(browse, 2, 0);
        root.Controls.Add(outputRow, 0, 3);
        var baselineRow = Table(3);
        baselineRow.ColumnStyles.Add(new(SizeType.Absolute, 120));
        baselineRow.ColumnStyles.Add(new(SizeType.Percent, 100));
        baselineRow.ColumnStyles.Add(new(SizeType.Absolute, 110));
        baselineRow.Controls.Add(Caption("Installed mods"), 0, 0);
        baselineRow.Controls.Add(installedFolder, 1, 0);
        var chooseInstalled = MakeButton("Browse...", 100);
        chooseInstalled.Click += (_, _) =>
        {
            using var dialog = new FolderBrowserDialog { Description = "Choose installed mods to compare (read only)", UseDescriptionForTitle = true, SelectedPath = installedFolder.Text };
            if (dialog.ShowDialog(this) == DialogResult.OK) { installedFolder.Text = dialog.SelectedPath; allowReviewedChanges.Checked = false; }
        };
        baselineRow.Controls.Add(chooseInstalled, 2, 0);
        root.Controls.Add(baselineRow, 0, 4);
        var policy = new FlowLayoutPanel { Dock = DockStyle.Fill, WrapContents = false, Margin = Padding.Empty, AutoScroll = true };
        policy.Controls.AddRange([dependencies, refresh, allowReviewedChanges]);
        root.Controls.Add(policy, 0, 5);

        var split = new SplitContainer
        {
            Dock = DockStyle.Fill, Size = new Size(1280, 500), SplitterDistance = 760,
            Panel1MinSize = 550, Panel2MinSize = 380, SplitterWidth = 12, BackColor = BackColor
        };
        split.Panel1.Controls.Add(BuildBrowser());
        split.Panel2.Controls.Add(BuildBasket());
        root.Controls.Add(split, 0, 6);
        root.Controls.Add(BuildFooter(), 0, 7);
        lockedDuringDownload.AddRange([serverAddress, serverToken, check, minecraftVersion, forgeVersion, channel, dependencies, refresh, output, browse, installedFolder, chooseInstalled, allowReviewedChanges]);
        LoadSettings();
        PopulateForgeVersions();
        if (args.Length == 1 && File.Exists(args[0])) ImportFile(args[0]);
        Shown += async (_, _) => await InitializeBrowserAsync();
        FormClosing += (_, e) =>
        {
            if (busy)
            {
                operation?.Cancel();
                e.Cancel = true;
                WriteLog("Canceling the operation. You can close the window once it finishes.");
                return;
            }
            SaveSettings();
        };
        AutoScaleDimensions = new SizeF(96F, 96F);
        AutoScaleMode = AutoScaleMode.Dpi;
    }

    private static Button MakeButton(string text, int width) => new()
    {
        Text = text, Width = width, Height = 32, FlatStyle = FlatStyle.System, AutoSize = false, Anchor = AnchorStyles.Left | AnchorStyles.Right,
        Margin = new Padding(3)
    };
    private static Label Caption(string text) => new()
    {
        Text = text, Dock = DockStyle.Fill, TextAlign = ContentAlignment.MiddleLeft, Margin = new Padding(3, 0, 6, 0), AutoEllipsis = true
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
        panel.ColumnStyles.Add(new(SizeType.Percent, 100));
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
        var go = MakeButton("Go", 50);
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
        var addCurrent = MakeButton("Add current project", 160);
        addCurrent.Click += (_, _) => AddUrl(web.Source?.AbsoluteUri ?? browserAddress.Text);
        picker.Controls.AddRange([pickLinks, addCurrent]);
        panel.Controls.Add(picker, 0, 1);
        panel.Controls.Add(web, 0, 2);
        return panel;
    }

    private Control BuildBasket()
    {
        var panel = new TableLayoutPanel { Dock = DockStyle.Fill, RowCount = 5, ColumnCount = 1, BackColor = Color.White, Padding = new Padding(10) };
        panel.ColumnStyles.Add(new(SizeType.Percent, 100));
        panel.RowStyles.Add(new(SizeType.Absolute, 34));
        panel.RowStyles.Add(new(SizeType.Absolute, 42));
        panel.RowStyles.Add(new(SizeType.Absolute, 42));
        panel.RowStyles.Add(new(SizeType.Percent, 100));
        panel.RowStyles.Add(new(SizeType.Absolute, 42));
        panel.Controls.Add(basketCount, 0, 0);
        var addRow = Table(2);
        addRow.ColumnStyles.Add(new(SizeType.Percent, 100));
        addRow.ColumnStyles.Add(new(SizeType.Absolute, 62));
        var add = MakeButton("Add", 56);
        add.Click += (_, _) => { if (AddUrl(projectInput.Text)) projectInput.Clear(); };
        projectInput.KeyDown += (_, e) => { if (e.KeyCode == Keys.Enter) { e.SuppressKeyPress = true; add.PerformClick(); } };
        addRow.Controls.Add(projectInput, 0, 0);
        addRow.Controls.Add(add, 1, 0);
        panel.Controls.Add(addRow, 0, 1);
        var files = new FlowLayoutPanel { Dock = DockStyle.Fill, WrapContents = false, Margin = Padding.Empty };
        var import = MakeButton("Import list", 128);
        var export = MakeButton("Save list", 105);
        import.Click += (_, _) =>
        {
            using var dialog = new OpenFileDialog { Title = "Import mod list", Filter = "Mod lists|.mods;.mods.example;*.mods;*.txt;*.list|All files|*.*", CheckFileExists = true };
            if (dialog.ShowDialog(this) == DialogResult.OK) ImportFile(dialog.FileName);
        };
        export.Click += (_, _) =>
        {
            using var dialog = new SaveFileDialog { Title = "Save mod list", Filter = "Mod lists|*.*", FileName = ".mods", AddExtension = false };
            if (dialog.ShowDialog(this) != DialogResult.OK) return;
            try { File.WriteAllLines(dialog.FileName, basket.Items.Cast<ProjectItem>().Select(x => x.Url), new UTF8Encoding(false)); WriteLog("Mod list saved."); }
            catch (Exception error) { WriteLog("Could not save the mod list: " + error.Message); }
        };
        files.Controls.AddRange([import, export]);
        panel.Controls.Add(files, 0, 2);
        panel.Controls.Add(basket, 0, 3);
        var edit = new FlowLayoutPanel { Dock = DockStyle.Fill, WrapContents = false, Margin = Padding.Empty };
        var remove = MakeButton("Remove", 105);
        var clear = MakeButton("Clear all", 110);
        remove.Click += (_, _) => { foreach (var index in basket.SelectedIndices.Cast<int>().OrderDescending()) basket.Items.RemoveAt(index); UpdateCount(); };
        clear.Click += (_, _) => { basket.Items.Clear(); UpdateCount(); };
        edit.Controls.AddRange([remove, clear]);
        panel.Controls.Add(edit, 0, 4);
        var selectedTab = new TabPage("Selected mods") { Padding = new Padding(3) };
        selectedTab.Controls.Add(panel);
        reportGrid.DefaultCellStyle.WrapMode = DataGridViewTriState.True;
        reportGrid.SizeChanged += (_, _) => reportGrid.AutoResizeRows();
        reportGrid.ColumnWidthChanged += (_, _) => reportGrid.AutoResizeRows();
        reportGrid.Columns.Add(new DataGridViewTextBoxColumn { Name = "Severity", HeaderText = "Type", FillWeight = 18, MinimumWidth = 65 });
        reportGrid.Columns.Add(new DataGridViewTextBoxColumn { Name = "Project", HeaderText = "Mod / project", FillWeight = 28, MinimumWidth = 85 });
        reportGrid.Columns.Add(new DataGridViewTextBoxColumn { Name = "Detail", HeaderText = "Details", FillWeight = 54, MinimumWidth = 120 });
        var reportDetails = new TextBox
        {
            Dock = DockStyle.Fill, Multiline = true, ReadOnly = true, ScrollBars = ScrollBars.Vertical,
            WordWrap = true, BackColor = Color.White, BorderStyle = BorderStyle.None
        };
        reportGrid.SelectionChanged += (_, _) =>
        {
            var row = reportGrid.CurrentRow;
            reportDetails.Text = row == null ? "" : string.Join(Environment.NewLine,
                row.Cells.Cast<DataGridViewCell>().Select(cell => Convert.ToString(cell.Value) ?? ""))
                .Replace("\r\n", "\n").Replace("\n", Environment.NewLine);
        };
        var detailGroup = new GroupBox { Text = "Selected report item", Dock = DockStyle.Fill, Padding = new Padding(8) };
        detailGroup.Controls.Add(reportDetails);
        var reportLayout = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 1, RowCount = 2, Margin = Padding.Empty };
        reportLayout.ColumnStyles.Add(new(SizeType.Percent, 100));
        reportLayout.RowStyles.Add(new(SizeType.Percent, 65));
        reportLayout.RowStyles.Add(new(SizeType.Percent, 35));
        reportLayout.Controls.Add(reportGrid, 0, 0);
        reportLayout.Controls.Add(detailGroup, 0, 1);
        reportTab.Controls.Add(reportLayout);
        basketTabs.TabPages.AddRange([selectedTab, reportTab]);
        return basketTabs;
    }

    private void ShowReport(JobSnapshot job)
    {
        reportGrid.Rows.Clear();
        foreach (var error in job.Errors) reportGrid.Rows.Add("Error", error.Project, error.Error);
        foreach (var warning in job.Warnings) reportGrid.Rows.Add("Warning", warning.Project, warning.Error);
        foreach (var change in job.Compatibility?.Changes ?? [])
            reportGrid.Rows.Add(change.Kind, string.IsNullOrEmpty(change.ModId) ? change.Previous : change.ModId, string.Join(Environment.NewLine, $"{change.OldVersion} → {change.NewVersion}", change.Previous, change.Current));
        foreach (var file in job.Files.Where(x => x.Verified != null))
        {
            var versions = string.Join(", ", file.Metadata?.Mods.Where(x => !x.Bundled).Select(x => $"{x.Id} {x.Version}") ?? []);
            var reason = file.Requested ? "Requested" : $"Required by: {string.Join(", ", file.RequiredBy)}";
            reportGrid.Rows.Add("Selected", file.Slug, string.Join(Environment.NewLine, versions, file.Filename, reason));
        }
        if (job.Errors.Length > 0 || job.Compatibility?.RequiresReview == true) basketTabs.SelectedTab = reportTab;
    }

    private Control BuildFooter()
    {
        var panel = new TableLayoutPanel { Dock = DockStyle.Fill, RowCount = 3, ColumnCount = 1, Padding = new Padding(0, 8, 0, 0) };
        panel.ColumnStyles.Add(new(SizeType.Percent, 100));
        panel.RowStyles.Add(new(SizeType.Absolute, 40));
        panel.RowStyles.Add(new(SizeType.Absolute, 16));
        panel.RowStyles.Add(new(SizeType.Percent, 100));
        var actions = Table(4);
        actions.ColumnStyles.Add(new(SizeType.Percent, 100));
        actions.ColumnStyles.Add(new(SizeType.Absolute, 118));
        actions.ColumnStyles.Add(new(SizeType.Absolute, 188));
        actions.ColumnStyles.Add(new(SizeType.Absolute, 78));
        actions.Controls.Add(status, 0, 0);
        var open = MakeButton("Open folder", 112);
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
            var environment = await CoreWebView2Environment.CreateAsync(
                userDataFolder: profile, options: new CoreWebView2EnvironmentOptions { Language = "en-US" });
            await web.EnsureCoreWebView2Async(environment).WaitAsync(TimeSpan.FromSeconds(30));
            web.CoreWebView2.Settings.AreHostObjectsAllowed = false;
            web.CoreWebView2.Settings.IsWebMessageEnabled = false;
            web.CoreWebView2.PermissionRequested += (_, e) => e.State = CoreWebView2PermissionState.Deny;
            web.CoreWebView2.DownloadStarting += (_, e) => { e.Cancel = true; WriteLog("Add the project to the list, then download it from the server."); };
            web.CoreWebView2.NavigationStarting += (_, e) =>
            {
                if (!ProjectUrls.IsCurseForgePage(e.Uri)) { e.Cancel = true; WriteLog("The browser can only open CurseForge HTTPS pages."); return; }
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
            WriteLog("Could not start the browser: " + error.Message + " · Check that Microsoft Edge WebView2 Runtime is installed. You can still import lists and enter URLs.");
        }
        if (smokeDirectory != null)
        {
            await Task.WhenAny(navigation.Task, Task.Delay(15000));
            await Task.Delay(1000);
            Directory.CreateDirectory(smokeDirectory);
            using var bitmap = new Bitmap(Width, Height);
            DrawToBitmap(bitmap, new Rectangle(Point.Empty, Size));
            bitmap.Save(Path.Combine(smokeDirectory, "client.png"));
            ShowReport(new JobSnapshot
            {
                Errors = [new JobIssue("compatibility-example", "A required mod version does not match the selected candidate. Pin a compatible file and retry.")],
                Warnings = [new JobIssue("baseline-example", "An installed mod is missing from this selection. Review replacements before continuing.")],
                Compatibility = new CompatibilityReport
                {
                    State = "review_required", RequiresReview = true,
                    Changes = [new ModChange("downgrade", "example_library", "previous-example-library-1.2.43.jar", "candidate-example-library-0.0.47-snapshot.jar", "1.2.43", "0.0.47-snapshot")]
                }
            });
            using (var reportImage = new Bitmap(Width, Height))
            {
                DrawToBitmap(reportImage, new Rectangle(Point.Empty, Size));
                reportImage.Save(Path.Combine(smokeDirectory, "report.png"));
            }
            Size = MinimumSize;
            PerformLayout();
            await Task.Delay(150);
            reportGrid.AutoResizeRows();
            using (var minimumImage = new Bitmap(Width, Height))
            {
                DrawToBitmap(minimumImage, new Rectangle(Point.Empty, Size));
                minimumImage.Save(Path.Combine(smokeDirectory, "minimum.png"));
            }
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
        if (!ProjectUrls.IsCurseForgePage(url)) { WriteLog("Enter a CurseForge HTTPS address."); return; }
        web.CoreWebView2?.Navigate(url);
    }

    private bool AddUrl(string raw)
    {
        if (busy) { WriteLog("Cancel the current download before changing the mod list."); return false; }
        try
        {
            var url = ProjectUrls.Normalize(raw);
            var existing = basket.Items.Cast<ProjectItem>().FirstOrDefault(x => ProjectUrls.ProjectKey(x.Url) == ProjectUrls.ProjectKey(url));
            if (existing != null)
            {
                if (existing.Url == url || !url.Contains("/files/")) { status.Text = "This mod is already in the list."; return true; }
                basket.Items.Remove(existing);
            }
            if (basket.Items.Count >= 500) throw new FormatException("You can add up to 500 projects at a time.");
            basket.Items.Add(new ProjectItem(url));
            allowReviewedChanges.Checked = false;
            UpdateCount();
            status.Text = $"Added to the list: {ProjectUrls.ProjectKey(url)}";
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
            if (count > 500) throw new FormatException("A mod list can contain up to 500 projects.");
            foreach (var url in urls) AddUrl(url);
            WriteLog($"Imported {urls.Length} projects from {Path.GetFileName(path)}.");
        }
        catch (Exception error) { WriteLog("Could not import the mod list: " + error.Message); }
    }

    private void UpdateCount() => basketCount.Text = $"Selected mods · {basket.Items.Count}";
    private void PopulateForgeVersions()
    {
        var selected = forgeVersion.Text;
        forgeVersion.Items.Clear();
        forgeVersion.Items.AddRange(ForgeVersions.InstalledFor(minecraftVersion.Text.Trim()));
        forgeVersion.Text = selected;
    }
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
            WriteLog("Connected to the server. Ready to download Forge mods.");
        }
        catch (Exception error) { WriteLog("Connection check failed: " + error.Message); }
        finally { if (!busy) button.Enabled = true; }
    }

    private async Task DownloadAsync()
    {
        if (busy) return;
        if (basket.Items.Count == 0) { WriteLog("Add mods to the list first."); return; }
        if (string.IsNullOrWhiteSpace(forgeVersion.Text)) { WriteLog("Select or enter your installed Forge version before downloading."); forgeVersion.Focus(); return; }
        using var cancellation = new CancellationTokenSource();
        operation = cancellation;
        busy = true;
        basketTabs.TabPages[0].Enabled = false;
        download.Enabled = false;
        cancel.Enabled = true;
        foreach (var control in lockedDuringDownload) control.Enabled = false;
        progress.Value = 0;
        reportGrid.Rows.Clear();
        ServerClient? api = null;
        string? jobId = null;
        var terminal = false;
        try
        {
            var directory = Path.GetFullPath(output.Text);
            if (directory == Path.GetPathRoot(directory)) throw new IOException("Choose a dedicated download folder.");
            api = new ServerClient(serverAddress.Text, serverToken.Text);
            var projectUrls = basket.Items.Cast<ProjectItem>().Select(x => x.Url).ToArray();
            var baselinePath = installedFolder.Text.Trim();
            if (baselinePath == "" && Directory.Exists(directory) && Directory.GetFiles(directory, "*.jar").Length > 0) baselinePath = directory;
            progress.Style = ProgressBarStyle.Marquee;
            var baseline = await BaselineReader.ReadAsync(baselinePath, new Progress<string>(message => status.Text = message), cancellation.Token);
            var request = new JobRequest(projectUrls,
                minecraftVersion.Text.Trim(), channel.Text, dependencies.Checked, refresh.Checked)
            {
                ForgeVersion = forgeVersion.Text.Trim(), InstalledFiles = baseline,
                AllowReviewedChanges = allowReviewedChanges.Checked
            };
            SaveSettings();
            WriteLog("Mod list sent to the server.");
            var job = await api.CreateJobAsync(request, cancellation.Token);
            jobId = job.Id;
            var issues = new List<JobIssue>();
            var localFiles = new List<JarFile>();
            var transferred = new HashSet<string>(StringComparer.Ordinal);
            while (true)
            {
                terminal = job.IsTerminal;
                if (job.State == "cancelled") throw new OperationCanceledException("The server job was canceled.");
                if (terminal)
                {
                    progress.Style = ProgressBarStyle.Continuous;
                    ShowReport(job);
                    if (job.State == "completed" && Directory.Exists(directory))
                    {
                        var filenames = job.Files.Select(x => x.Filename).ToHashSet(StringComparer.OrdinalIgnoreCase);
                        var unselected = Directory.GetFiles(directory, "*.jar").Where(x => !filenames.Contains(Path.GetFileName(x))).ToArray();
                        if (unselected.Length > 0) throw new IOException("The output folder contains JARs outside the validated selection. Choose an empty output folder to prevent duplicate or obsolete mods.");
                    }
                }
                // Ready paths are released only after the whole batch passes validation.
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
                        status.Text = $"Downloading {localCount + 1}/{snapshot.Total}: {value.Filename} · {value.Received / 1048576.0:F1}/{value.Total / 1048576.0:F1} MiB";
                    });
                    try
                    {
                        var result = await api.DownloadAsync(file, directory, reporter, cancellation.Token);
                        localFiles.Add(file with { Status = result });
                        WriteLog($"{(result == "cached" ? "Reused local file" : "Downloaded")}: {file.Filename}");
                    }
                    catch (Exception error) when (error is IOException or HttpRequestException or UnauthorizedAccessException)
                    {
                        issues.Add(new JobIssue(file.Slug, error.Message));
                        localFiles.Add(file with { Status = "failed", Verified = null, Error = error.Message });
                        WriteLog($"File download failed: {file.Filename} · {error.Message}");
                    }
                }
                if (terminal) break;
                status.Text = job.Detail != "" ? job.Detail : job.State is "queued" or "resolving" ? "Finding the highest compatible mod versions. The first scan may take longer." :
                    $"Validating requirements: {job.Completed}/{job.Total} · {job.CacheHits} cache hits";
                progress.Style = job.Total == 0 ? ProgressBarStyle.Marquee : ProgressBarStyle.Continuous;
                progress.Value = job.Total == 0 ? 0 : Math.Clamp((job.Completed + localFiles.Count) * 500 / job.Total, 0, 1000);
                await Task.Delay(600, cancellation.Token);
                job = await api.GetJobAsync(job.Id, cancellation.Token);
            }
            issues.AddRange(job.Errors);
            foreach (var issue in job.Errors) WriteLog($"{issue.Project}: {issue.Error}");
            foreach (var warning in job.Warnings) WriteLog($"Warning · {warning.Project}: {warning.Error}");
            localFiles.AddRange(job.Files.Where(x => !transferred.Contains(x.DownloadPath)).Select(x =>
                x.Status == "failed" ? x : x with { Status = "blocked", Verified = null, Error = "Batch compatibility checks did not pass." }));
            await ServerClient.WriteManifestAsync(directory, job, localFiles.ToArray(), issues.ToArray(), cancellation.Token);
            progress.Value = 1000;
            WriteLog(issues.Count == 0 ? $"Complete: {localFiles.Count} JAR files saved · {job.Warnings.Length} warning(s). See Validation report." :
                $"{issues.Count} issue(s). No incompatible batch was released. See Validation report and the failed-job report.");
        }
        catch (OperationCanceledException)
        {
            WriteLog("Download canceled. Verified files have been kept in the output folder.");
            if (!terminal && api != null && jobId != null)
            {
                try { using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(5)); await api.CancelJobAsync(jobId, timeout.Token); }
                catch (Exception) { }
            }
        }
        catch (Exception error) { WriteLog("Download failed: " + error.Message); }
        finally
        {
            api?.Dispose();
            operation = null;
            busy = false;
            basketTabs.TabPages[0].Enabled = true;
            download.Enabled = true;
            cancel.Enabled = false;
            allowReviewedChanges.Checked = false;
            progress.Style = ProgressBarStyle.Continuous;
            foreach (var control in lockedDuringDownload) control.Enabled = true;
        }
    }

    private void SetDefaultOutput() => output.Text = Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.MyDocuments), "cfmodsync", "downloads",
        minecraftVersion.Text.Trim() + "-forge" + (forgeVersion.Text.Trim() == "" ? "" : "-" + forgeVersion.Text.Trim()));

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
            forgeVersion.Text = settings.ForgeVersion;
            installedFolder.Text = settings.InstalledFolder;
            channel.SelectedItem = settings.Channel is "all" or "beta" or "release" ? settings.Channel : "release";
            if (settings.SelectionPolicyVersion < 2 && settings.Channel == "all")
            {
                channel.SelectedItem = "release";
                WriteLog("Release-only is now the default. Select beta or all explicitly if needed.");
            }
            dependencies.Checked = settings.Dependencies;
            customOutput = settings.CustomOutput;
            if (customOutput) output.Text = settings.Output;
            else SetDefaultOutput();
            foreach (var url in settings.ProjectUrls.Take(500)) AddUrl(url);
        }
        catch (Exception error) { WriteLog("Could not load saved settings: " + error.Message); }
        finally { loadingSettings = false; }
    }

    private void SaveSettings()
    {
        if (smokeDirectory != null) return;
        try
        {
            Directory.CreateDirectory(AppDirectory);
            var settings = new ClientSettings(serverAddress.Text, minecraftVersion.Text, channel.Text,
                dependencies.Checked, output.Text, customOutput, basket.Items.Cast<ProjectItem>().Select(x => x.Url).ToArray())
            { ForgeVersion = forgeVersion.Text, InstalledFolder = installedFolder.Text, SelectionPolicyVersion = 2 };
            File.WriteAllText(SettingsPath, JsonSerializer.Serialize(settings, Protocol.Json));
        }
        catch (Exception error) { WriteLog("Could not save settings: " + error.Message); }
    }

    private sealed record ProjectItem(string Url)
    {
        public override string ToString() => ProjectUrls.ProjectKey(Url) + (Url.Contains("/files/") ? $" · pinned {Url.Split('/')[^1]}" : "");
    }
    private sealed record ClientSettings(string ServerAddress, string MinecraftVersion, string Channel,
        bool Dependencies, string Output, bool CustomOutput, string[] ProjectUrls)
    {
        public string ForgeVersion { get; init; } = "";
        public string InstalledFolder { get; init; } = "";
        public int SelectionPolicyVersion { get; init; }
    }
}
