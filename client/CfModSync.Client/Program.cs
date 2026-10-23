using System.Globalization;

namespace CfModSync.Client;

internal static class Program
{
    [STAThread]
    private static void Main(string[] args)
    {
        var culture = CultureInfo.GetCultureInfo("en-US");
        CultureInfo.DefaultThreadCurrentCulture = culture;
        CultureInfo.DefaultThreadCurrentUICulture = culture;
        CultureInfo.CurrentCulture = culture;
        CultureInfo.CurrentUICulture = culture;
        ApplicationConfiguration.Initialize();
        Application.Run(new MainForm(args));
    }
}
