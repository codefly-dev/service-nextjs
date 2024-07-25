import Layout from '../components/layout';
import logo from '../assets/icon-mdpi.png'; // Adjust the path to your image file

const Home = () => {
    return (
        <Layout>
            <div className="grid gap-[30px] flex items-center justify-center mt-40">
                <h1>Welcome to Codefly</h1>
                <img src={logo} alt="Description of the image" className="w-1/2 h-auto" />
            </div>
        </Layout>
    );
};

export default Home;