import Head from "next/head";
import Header from "./header";
import { Sidebar } from "./sidebar";
import { Inter } from "next/font/google";
import { ThemeProvider } from "./theme-provider";

type LayoutProps = {
  user?: any;
  loading?: boolean;
  children: React.ReactNode;
};

const inter = Inter({
  subsets: ["latin"],
  weight: ["300", "400", "500", "700"],
});

const Layout = ({ user, loading = false, children }: LayoutProps) => {
  return (
    <>
      <Head>
        <title>Next.js with Auth0</title>
      </Head>

      <ThemeProvider>
        <main className={inter.className}>
          <Header user={user} loading={loading} />
          <div className="max-w-[672px] my-[1.5rem] mx-auto">{children}</div>
        </main>
      </ThemeProvider>
    </>
  );
};

export default Layout;
