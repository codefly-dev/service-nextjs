import Head from "next/head";
import Header from "./header";
import { Inter } from "next/font/google";
import { ThemeProvider } from "./theme-provider";

type LayoutProps = {
  loading?: boolean;
  children: React.ReactNode;
};

const inter = Inter({
  subsets: ["latin"],
  weight: ["300", "400", "500", "700"],
});

const Layout = ({ loading = false, children }: LayoutProps) => {
  return (
    <>
      <Head>
        <title>codefly</title>
      </Head>

      <ThemeProvider>
        <main className={inter.className}>
          <Header />
          <div className="my-[1.5rem] mx-auto">{children}</div>
        </main>
      </ThemeProvider>
      
    </>
  );
};

export default Layout;
